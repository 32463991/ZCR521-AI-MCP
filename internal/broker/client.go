package broker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"github.com/zcr521/android-ai-mcp/internal/model"
)

type Client struct {
	SocketPath      string
	DialTimeout     time.Duration
	MaxResponseSize int64
}

func (c Client) Call(ctx context.Context, tool string, args map[string]any) model.Result {
	response, err := c.request(ctx, "tools/call", tool, args)
	if err != nil {
		return model.Failure("BROKER_UNAVAILABLE", "无法连接 Root broker", "BrokerError", err.Error())
	}
	return response.Result
}

func (c Client) Status(ctx context.Context) (map[string]any, error) {
	response, err := c.request(ctx, "status", "", map[string]any{})
	if err != nil {
		return nil, err
	}
	data, ok := response.Data.(map[string]any)
	if !ok {
		return nil, errors.New("broker returned invalid status shape")
	}
	return data, nil
}

func (c Client) TaskGet(ctx context.Context, id string) (any, error) {
	response, err := c.request(ctx, "tasks/get", "", map[string]any{"taskId": id})
	return response.Data, err
}

func (c Client) TaskList(ctx context.Context) (any, error) {
	response, err := c.request(ctx, "tasks/list", "", map[string]any{})
	return response.Data, err
}

func (c Client) TaskUpdate(ctx context.Context, id string, progress float64, message string) (any, error) {
	response, err := c.request(ctx, "tasks/update", "", map[string]any{
		"taskId": id, "progress": progress, "message": message,
	})
	return response.Data, err
}

func (c Client) TaskCancel(ctx context.Context, id string) (any, error) {
	response, err := c.request(ctx, "tasks/cancel", "", map[string]any{"taskId": id})
	return response.Data, err
}

func (c Client) request(ctx context.Context, method, tool string, args map[string]any) (Response, error) {
	if c.SocketPath == "" {
		return Response{}, errors.New("broker socket path is empty")
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return Response{}, fmt.Errorf("encode broker arguments: %w", err)
	}
	timeout := c.DialTimeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "unix", c.SocketPath)
	if err != nil {
		return Response{}, err
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	timeoutMS := int64(0)
	if deadline, ok := ctx.Deadline(); ok {
		timeoutMS = time.Until(deadline).Milliseconds()
		if timeoutMS < 1 {
			timeoutMS = 1
		}
	}
	request := Request{
		Version:   WireVersion,
		ID:        randomRequestID(),
		Method:    method,
		Tool:      tool,
		Arguments: raw,
		TimeoutMS: timeoutMS,
	}
	if err := json.NewEncoder(conn).Encode(request); err != nil {
		return Response{}, fmt.Errorf("write broker request: %w", err)
	}
	var response Response
	limit := c.MaxResponseSize
	if limit <= 0 {
		limit = 16 << 20
	}
	decoder := json.NewDecoder(&limitedReader{reader: conn, remaining: limit})
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil {
		return Response{}, fmt.Errorf("read broker response: %w", err)
	}
	if response.Version != WireVersion || response.ID != request.ID {
		return Response{}, errors.New("broker response identity mismatch")
	}
	if response.Error != "" {
		if response.ErrorCode == "NOT_FOUND" {
			return response, fmt.Errorf("%w: %s", os.ErrNotExist, response.Error)
		}
		return response, errors.New(response.Error)
	}
	return response, nil
}

type limitedReader struct {
	reader    net.Conn
	remaining int64
}

func (r *limitedReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, errors.New("broker response exceeds size limit")
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}

func randomRequestID() string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}
