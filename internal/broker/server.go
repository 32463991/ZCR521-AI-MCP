package broker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zcr521/android-ai-mcp/internal/model"
)

type ServerOptions struct {
	SocketPath             string
	FrontendPIDFile        string
	AllowedUIDs            []uint32
	SocketGID              int
	MaxRequestBytes        int64
	MaxConcurrent          int
	AllowUnverifiedForHost bool
}

type Server struct {
	options  ServerOptions
	handler  Handler
	listener net.Listener
	sem      chan struct{}
	wg       sync.WaitGroup
}

func NewServer(options ServerOptions, handler Handler) (*Server, error) {
	if options.SocketPath == "" {
		return nil, errors.New("broker socket path is required")
	}
	if handler == nil {
		return nil, errors.New("broker handler is required")
	}
	if len(options.AllowedUIDs) == 0 {
		options.AllowedUIDs = []uint32{0, 2000}
	}
	if options.MaxRequestBytes <= 0 {
		options.MaxRequestBytes = 16 << 20
	}
	if options.MaxConcurrent <= 0 {
		options.MaxConcurrent = 16
	}
	if options.SocketGID == 0 {
		options.SocketGID = 2000
	}
	return &Server{
		options: options,
		handler: handler,
		sem:     make(chan struct{}, options.MaxConcurrent),
	}, nil
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(s.options.SocketPath), 0o700); err != nil {
		return fmt.Errorf("create broker socket directory: %w", err)
	}
	if err := removeStaleSocket(s.options.SocketPath); err != nil {
		return err
	}
	listener, err := net.Listen("unix", s.options.SocketPath)
	if err != nil {
		return fmt.Errorf("listen broker socket: %w", err)
	}
	s.listener = listener
	if err := os.Chmod(s.options.SocketPath, 0o660); err != nil {
		_ = listener.Close()
		return fmt.Errorf("chmod broker socket: %w", err)
	}
	if !s.options.AllowUnverifiedForHost {
		if err := chownSocket(s.options.SocketPath, s.options.SocketGID); err != nil {
			_ = listener.Close()
			return fmt.Errorf("chown broker socket: %w", err)
		}
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(s.options.SocketPath)
		s.wg.Wait()
	}()
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept broker connection: %w", err)
		}
		select {
		case s.sem <- struct{}{}:
			s.wg.Add(1)
			go func() {
				defer func() {
					<-s.sem
					s.wg.Done()
				}()
				s.serveConn(ctx, conn)
			}()
		default:
			_ = writeResponse(conn, Response{
				Version: WireVersion,
				Result:  model.Failure("BUSY", "Root broker 并发已满", "ConcurrencyLimit", "too many broker calls"),
				Error:   "broker concurrency limit reached",
			})
			_ = conn.Close()
		}
	}
}

func (s *Server) serveConn(parent context.Context, connection net.Conn) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(24 * time.Hour))
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return
	}
	peer, err := inspectPeer(unixConnection)
	if err != nil && !s.options.AllowUnverifiedForHost {
		_ = writeResponse(connection, Response{
			Version: WireVersion,
			Result:  model.Failure("PEER_REJECTED", "无法验证 frontend 身份", "PeerCredentialError", err.Error()),
			Error:   "peer credential verification failed",
		})
		return
	}
	if err == nil && !s.options.AllowUnverifiedForHost {
		if err := s.authorizePeer(peer); err != nil {
			_ = writeResponse(connection, Response{
				Version: WireVersion,
				Result:  model.Failure("PEER_REJECTED", "拒绝未授权的 broker 调用", "PeerRejected", err.Error()),
				Error:   err.Error(),
			})
			return
		}
	}

	var request Request
	decoder := json.NewDecoder(io.LimitReader(connection, s.options.MaxRequestBytes))
	decoder.UseNumber()
	if err := decoder.Decode(&request); err != nil {
		_ = writeResponse(connection, Response{
			Version: WireVersion,
			Result:  model.Failure("INVALID_REQUEST", "broker 请求无效", "DecodeError", err.Error()),
			Error:   err.Error(),
		})
		return
	}
	response := Response{Version: WireVersion, ID: request.ID}
	if request.Version != WireVersion || request.ID == "" {
		response.Result = model.Failure("INVALID_REQUEST", "broker 协议版本或请求 ID 无效", "ProtocolError", "")
		response.Error = "invalid broker protocol"
		_ = writeResponse(connection, response)
		return
	}
	ctx := parent
	cancel := func() {}
	if request.TimeoutMS > 0 {
		ctx, cancel = context.WithTimeout(parent, min(time.Duration(request.TimeoutMS)*time.Millisecond, 24*time.Hour))
	}
	defer cancel()
	response = s.dispatch(ctx, request)
	_ = writeResponse(connection, response)
}

func (s *Server) dispatch(ctx context.Context, request Request) Response {
	response := Response{Version: WireVersion, ID: request.ID}
	args := map[string]any{}
	if len(request.Arguments) > 0 {
		if err := json.Unmarshal(request.Arguments, &args); err != nil {
			response.Result = model.Failure("INVALID_ARGUMENT", "参数不是 JSON 对象", "DecodeError", err.Error())
			response.Error = err.Error()
			return response
		}
	}
	var err error
	switch request.Method {
	case "tools/call":
		response.Result = s.handler.Call(ctx, request.Tool, args)
	case "status":
		response.Data, err = s.handler.Status(ctx)
	case "tasks/get":
		response.Data, err = s.handler.TaskGet(ctx, stringArgument(args, "taskId"))
	case "tasks/list":
		response.Data, err = s.handler.TaskList(ctx)
	case "tasks/update":
		response.Data, err = s.handler.TaskUpdate(
			ctx,
			stringArgument(args, "taskId"),
			numberArgument(args, "progress"),
			stringArgument(args, "message"),
		)
	case "tasks/cancel":
		response.Data, err = s.handler.TaskCancel(ctx, stringArgument(args, "taskId"))
	default:
		err = fmt.Errorf("unknown broker method %q", request.Method)
	}
	if err != nil {
		response.Result = model.Failure("BROKER_CALL_FAILED", "Root broker 调用失败", "BrokerCallError", err.Error())
		if errors.Is(err, os.ErrNotExist) {
			response.ErrorCode = "NOT_FOUND"
		}
		response.Error = err.Error()
	}
	return response
}

func (s *Server) authorizePeer(peer Peer) error {
	uidAllowed := false
	for _, uid := range s.options.AllowedUIDs {
		if peer.UID == uid {
			uidAllowed = true
			break
		}
	}
	if !uidAllowed {
		return fmt.Errorf("uid %d is not allowed", peer.UID)
	}
	data, err := os.ReadFile(s.options.FrontendPIDFile)
	if err != nil {
		return fmt.Errorf("read frontend pid: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 1 {
		return errors.New("frontend pid file is invalid")
	}
	if peer.PID != pid {
		return fmt.Errorf("peer pid %d does not match frontend child %d", peer.PID, pid)
	}
	return nil
}

func writeResponse(writer io.Writer, response Response) error {
	return json.NewEncoder(writer).Encode(response)
}

func removeStaleSocket(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect broker socket: %w", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to replace non-socket path %s", path)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("remove stale broker socket: %w", err)
	}
	return nil
}

func stringArgument(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return value
}

func numberArgument(args map[string]any, key string) float64 {
	switch value := args[key].(type) {
	case float64:
		return value
	case json.Number:
		number, _ := value.Float64()
		return number
	default:
		return 0
	}
}
