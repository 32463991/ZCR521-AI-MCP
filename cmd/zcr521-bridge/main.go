// zcr521-bridge adapts a local MCP stdio client to the phone's Streamable HTTP
// endpoint. Protocol messages are the only bytes ever written to stdout.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	defaultMCPURL       = "http://127.0.0.1:5322/mcp"
	defaultMessageLimit = 8 << 20
	bridgeVersion       = "0.01"
)

type config struct {
	endpoint       *url.URL
	requestTimeout time.Duration
	messageLimit   int
}

type bridge struct {
	config config
	client *http.Client
	ctx    context.Context
	cancel context.CancelFunc
	input  io.Reader
	output io.Writer
	logger *log.Logger

	outputMu sync.Mutex
	stateMu  sync.RWMutex
	session  string
	protocol string
	lastID   string
	stream   sync.Once
	posts    sync.WaitGroup
}

type messageInfo struct {
	ID              json.RawMessage
	HasID           bool
	Method          string
	Name            string
	ProtocolVersion string
	IsNotification  bool
}

func main() {
	endpointFlag := flag.String("url", envOr("ZCR521_MCP_URL", defaultMCPURL), "手机的 Streamable HTTP MCP 地址")
	timeoutFlag := flag.Duration("request-timeout", 10*time.Minute, "单个 HTTP MCP 请求超时，0 表示不限制")
	limitFlag := flag.Int("max-message-bytes", defaultMessageLimit, "单条 stdio JSON-RPC 消息上限")
	showVersion := flag.Bool("version", false, "显示桥接器版本")
	flag.Parse()
	if *showVersion {
		fmt.Println(bridgeVersion)
		return
	}
	endpoint, err := validateEndpoint(*endpointFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "zcr521-bridge:", err)
		os.Exit(2)
	}
	if *timeoutFlag < 0 || *limitFlag < 1024 {
		fmt.Fprintln(os.Stderr, "zcr521-bridge: 参数范围无效")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runner := newBridge(ctx, config{
		endpoint:       endpoint,
		requestTimeout: *timeoutFlag,
		messageLimit:   *limitFlag,
	}, os.Stdin, os.Stdout, os.Stderr)
	if err := runner.run(); err != nil && !errors.Is(err, context.Canceled) {
		runner.logger.Printf("桥接结束: %v", err)
		os.Exit(1)
	}
}

func newBridge(parent context.Context, cfg config, input io.Reader, output, logs io.Writer) *bridge {
	ctx, cancel := context.WithCancel(parent)
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	return &bridge{
		config: cfg,
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errors.New("MCP 端点不得重定向")
			},
		},
		ctx:    ctx,
		cancel: cancel,
		input:  input,
		output: output,
		logger: log.New(logs, "zcr521-bridge: ", log.LstdFlags),
	}
}

func (b *bridge) run() error {
	defer b.cancel()
	defer b.client.CloseIdleConnections()
	scanner := bufio.NewScanner(b.input)
	scanner.Buffer(make([]byte, 64*1024), b.config.messageLimit)
	for scanner.Scan() {
		raw := bytes.TrimSpace(bytes.Clone(scanner.Bytes()))
		if len(raw) == 0 {
			continue
		}
		info, err := inspectMessage(raw)
		if err != nil {
			b.logger.Printf("忽略无效 stdio 消息: %v", err)
			continue
		}
		// Initialization and notifications are ordered. Normal requests run
		// concurrently so a cancellation can overtake a long tool call.
		if info.Method == "initialize" || info.IsNotification {
			b.post(raw, info)
			continue
		}
		b.posts.Add(1)
		go func(payload []byte, details messageInfo) {
			defer b.posts.Done()
			b.post(payload, details)
		}(raw, info)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取 stdin: %w", err)
	}
	b.posts.Wait()
	b.deleteSession()
	return nil
}

func (b *bridge) post(raw []byte, info messageInfo) {
	ctx := b.ctx
	cancel := func() {}
	if b.config.requestTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, b.config.requestTimeout)
	}
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.config.endpoint.String(), bytes.NewReader(raw))
	if err != nil {
		b.transportFailure(info, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	b.applySessionHeaders(req)
	applyMessageHeaders(req, info)
	resp, err := b.client.Do(req)
	if err != nil {
		b.transportFailure(info, err)
		return
	}
	defer resp.Body.Close()
	newSession := strings.TrimSpace(resp.Header.Get("Mcp-Session-Id"))
	if newSession != "" {
		b.setSession(newSession)
	}
	if resp.StatusCode == http.StatusAccepted && !info.HasID {
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		message := fmt.Sprintf("HTTP %d", resp.StatusCode)
		if detail := strings.TrimSpace(string(body)); detail != "" {
			message += ": " + detail
		}
		b.transportFailure(info, errors.New(message))
		return
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
	switch mediaType {
	case "application/json":
		body, err := readBounded(resp.Body, int64(b.config.messageLimit))
		if err != nil {
			b.transportFailure(info, err)
			return
		}
		b.emit(body)
	case "text/event-stream":
		if err := b.consumeSSE(resp.Body, info.ID, info.HasID, true); err != nil &&
			!errors.Is(err, context.Canceled) {
			b.transportFailure(info, err)
			return
		}
	default:
		b.transportFailure(info, fmt.Errorf("不支持的响应 Content-Type %q", mediaType))
		return
	}
	if info.Method == "initialize" {
		b.startServerStream()
	}
}

func (b *bridge) consumeSSE(reader io.Reader, targetID json.RawMessage, hasTarget, stopAtTarget bool) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), b.config.messageLimit)
	var eventID string
	var data []string
	dispatch := func() (bool, error) {
		if eventID != "" {
			b.setLastEventID(eventID)
		}
		if len(data) == 0 {
			eventID = ""
			return false, nil
		}
		payload := []byte(strings.Join(data, "\n"))
		data = data[:0]
		eventID = ""
		if !json.Valid(payload) {
			return false, errors.New("SSE data 不是有效 JSON")
		}
		b.emit(payload)
		if stopAtTarget && hasTarget && responseMatchesID(payload, targetID) {
			return true, nil
		}
		return false, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			stop, err := dispatch()
			if err != nil || stop {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			value = ""
		} else {
			value = strings.TrimPrefix(value, " ")
		}
		switch field {
		case "id":
			if !strings.ContainsRune(value, '\x00') {
				eventID = value
			}
		case "data":
			data = append(data, value)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	_, err := dispatch()
	return err
}

func (b *bridge) startServerStream() {
	b.stream.Do(func() {
		go b.serverStream()
	})
}

func (b *bridge) serverStream() {
	backoff := 250 * time.Millisecond
	for b.ctx.Err() == nil {
		req, err := http.NewRequestWithContext(b.ctx, http.MethodGet, b.config.endpoint.String(), nil)
		if err != nil {
			b.logger.Printf("创建服务端流失败: %v", err)
			return
		}
		req.Header.Set("Accept", "text/event-stream")
		b.applySessionHeaders(req)
		if lastID := b.lastEventID(); lastID != "" {
			req.Header.Set("Last-Event-ID", lastID)
		}
		resp, err := b.client.Do(req)
		if err == nil && resp.StatusCode == http.StatusMethodNotAllowed {
			resp.Body.Close()
			return
		}
		if err == nil && resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			b.logger.Print("MCP 会话已失效；请让客户端重新初始化")
			return
		}
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			mediaType := strings.ToLower(strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))
			if mediaType != "text/event-stream" {
				err = fmt.Errorf("GET /mcp 返回 %q", mediaType)
			} else {
				err = b.consumeSSE(resp.Body, nil, false, false)
			}
			resp.Body.Close()
		} else if err == nil {
			err = fmt.Errorf("GET /mcp 返回 HTTP %d", resp.StatusCode)
			resp.Body.Close()
		}
		if b.ctx.Err() != nil {
			return
		}
		if err != nil {
			b.logger.Printf("服务端流断开，准备重连: %v", err)
		}
		timer := time.NewTimer(backoff)
		select {
		case <-b.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff = time.Duration(math.Min(float64(5*time.Second), float64(backoff*2)))
	}
}

func (b *bridge) emit(raw []byte) {
	compact := bytes.Buffer{}
	if err := json.Compact(&compact, raw); err != nil {
		b.logger.Printf("服务端返回无效 JSON: %v", err)
		return
	}
	b.captureProtocol(compact.Bytes())
	b.outputMu.Lock()
	defer b.outputMu.Unlock()
	_, _ = b.output.Write(compact.Bytes())
	_, _ = b.output.Write([]byte{'\n'})
}

func (b *bridge) transportFailure(info messageInfo, err error) {
	if !info.HasID {
		b.logger.Printf("通知转发失败: %v", err)
		return
	}
	message := err.Error()
	if len(message) > 1024 {
		message = message[:1024]
	}
	response := struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Error   struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{
		JSONRPC: "2.0",
		ID:      info.ID,
	}
	response.Error.Code = -32098
	response.Error.Message = "ZCR521 HTTP 桥接失败: " + message
	raw, marshalErr := json.Marshal(response)
	if marshalErr == nil {
		b.emit(raw)
	}
}

func (b *bridge) captureProtocol(raw []byte) {
	var response struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &response) == nil && response.Result.ProtocolVersion != "" {
		b.stateMu.Lock()
		b.protocol = response.Result.ProtocolVersion
		b.stateMu.Unlock()
	}
}

func (b *bridge) applySessionHeaders(req *http.Request) {
	b.stateMu.RLock()
	defer b.stateMu.RUnlock()
	if b.session != "" {
		req.Header.Set("Mcp-Session-Id", b.session)
	}
	if b.protocol != "" {
		req.Header.Set("MCP-Protocol-Version", b.protocol)
	}
}

func (b *bridge) setSession(session string) {
	b.stateMu.Lock()
	b.session = session
	b.stateMu.Unlock()
}

func (b *bridge) setLastEventID(id string) {
	b.stateMu.Lock()
	b.lastID = id
	b.stateMu.Unlock()
}

func (b *bridge) lastEventID() string {
	b.stateMu.RLock()
	defer b.stateMu.RUnlock()
	return b.lastID
}

func (b *bridge) deleteSession() {
	b.stateMu.RLock()
	session := b.session
	protocol := b.protocol
	b.stateMu.RUnlock()
	if session == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, b.config.endpoint.String(), nil)
	if err != nil {
		return
	}
	req.Header.Set("Mcp-Session-Id", session)
	if protocol != "" {
		req.Header.Set("MCP-Protocol-Version", protocol)
	}
	resp, err := b.client.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

func inspectMessage(raw []byte) (messageInfo, error) {
	if !json.Valid(raw) {
		return messageInfo{}, errors.New("不是有效 JSON")
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return messageInfo{}, errors.New("stdio 传输只接受 JSON-RPC 对象")
	}
	var info messageInfo
	if method, ok := object["method"]; ok {
		_ = json.Unmarshal(method, &info.Method)
	}
	if params, ok := object["params"]; ok {
		var details struct {
			Name string `json:"name"`
			URI  string `json:"uri"`
			Meta struct {
				ProtocolVersion string `json:"protocolVersion"`
			} `json:"_meta"`
		}
		if json.Unmarshal(params, &details) == nil {
			info.ProtocolVersion = details.Meta.ProtocolVersion
			switch info.Method {
			case "tools/call", "prompts/get":
				info.Name = details.Name
			case "resources/read":
				info.Name = details.URI
			}
		}
	}
	if id, ok := object["id"]; ok && string(id) != "null" {
		info.ID = bytes.Clone(id)
		info.HasID = true
	}
	info.IsNotification = info.Method != "" && !info.HasID
	return info, nil
}

func applyMessageHeaders(request *http.Request, info messageInfo) {
	version := info.ProtocolVersion
	if version == "" {
		version = request.Header.Get("MCP-Protocol-Version")
	}
	if version < "2026-07-28" {
		return
	}
	request.Header.Set("MCP-Protocol-Version", version)
	if info.Method != "" {
		request.Header.Set("Mcp-Method", info.Method)
	}
	if info.Name != "" {
		request.Header.Set("Mcp-Name", info.Name)
	}
}

func responseMatchesID(raw, target json.RawMessage) bool {
	var object struct {
		ID json.RawMessage `json:"id"`
	}
	if json.Unmarshal(raw, &object) != nil || len(object.ID) == 0 {
		return false
	}
	left := bytes.Buffer{}
	right := bytes.Buffer{}
	if json.Compact(&left, object.ID) != nil || json.Compact(&right, target) != nil {
		return false
	}
	return bytes.Equal(left.Bytes(), right.Bytes())
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("读取限制无效")
	}
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("HTTP MCP 响应超过消息上限")
	}
	return data, nil
}

func validateEndpoint(raw string) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("MCP URL 无效: %w", err)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, errors.New("MCP URL 必须使用 http 或 https")
	}
	if endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, errors.New("MCP URL 不得包含凭据、查询参数或片段")
	}
	if endpoint.Path == "" || endpoint.Path == "/" {
		endpoint.Path = "/mcp"
	}
	return endpoint, nil
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
