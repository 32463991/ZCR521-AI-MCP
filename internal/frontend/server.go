package frontend

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/zcr521/android-ai-mcp/internal/webui"
)

const (
	defaultPort             = 5322
	defaultWorkDir          = "/storage/emulated/0/zcr521AI"
	defaultMaxRequestBytes  = int64(8 << 20)
	defaultMaxUploadBytes   = int64(8 << 30)
	defaultTransferChunk    = int64(4 << 20)
	defaultMaxConcurrent    = 32
	anonymousRootLANWarning = "安全警告：此服务在局域网内匿名提供完整 Root 能力；同一网络中的任何设备都可能控制本机。请勿连接不可信 Wi-Fi，且不要将端口映射到公网。"
)

// Server is the complete public HTTP frontend.
type Server struct {
	options   Options
	mux       *http.ServeMux
	access    *accessPolicy
	slots     chan struct{}
	transfers *TransferManager
}

// New validates all mandatory integration points and constructs a frontend.
func New(options Options) (*Server, error) {
	if options.Broker == nil {
		return nil, errors.New("frontend: BrokerClient 未配置")
	}
	if options.MCP.SDKStreamable == nil {
		return nil, errors.New("frontend: 官方 SDK Streamable HTTP handler 未配置")
	}
	if options.MCP.SDKCurrent == nil {
		return nil, errors.New("frontend: 官方 SDK 2026 Streamable HTTP handler 未配置")
	}
	if options.EnableLegacySSE &&
		(options.MCP.LegacySSE == nil || options.MCP.LegacyMessages == nil) {
		return nil, errors.New("frontend: legacy SSE 已启用但 handler 不完整")
	}
	applyDefaults(&options)
	transfers, err := NewTransferManager(TransferOptions{
		Directory:      options.TransferDir,
		MaxUploadBytes: options.MaxUploadBytes,
		MaxChunkBytes:  options.TransferChunkBytes,
		UploadTTL:      options.UploadTTL,
		ArtifactTTL:    options.ArtifactTTL,
	})
	if err != nil {
		return nil, fmt.Errorf("frontend: 初始化传输目录: %w", err)
	}
	server := &Server{
		options:   options,
		mux:       http.NewServeMux(),
		access:    newAccessPolicy(options.ListenAddr, options.AllowedOrigins),
		slots:     make(chan struct{}, options.MaxConcurrent),
		transfers: transfers,
	}
	server.routes()
	return server, nil
}

func applyDefaults(options *Options) {
	if options.Port == 0 {
		options.Port = defaultPort
	}
	if options.ListenAddr == "" {
		options.ListenAddr = "0.0.0.0"
	}
	if options.WorkDir == "" {
		options.WorkDir = defaultWorkDir
	}
	if options.TransferDir == "" {
		options.TransferDir = options.WorkDir + "/uploads/.transfer"
	}
	if options.MaxRequestBytes <= 0 {
		options.MaxRequestBytes = defaultMaxRequestBytes
	}
	if options.MaxUploadBytes <= 0 {
		options.MaxUploadBytes = defaultMaxUploadBytes
	}
	if options.TransferChunkBytes <= 0 {
		options.TransferChunkBytes = defaultTransferChunk
	}
	if options.MaxConcurrent <= 0 {
		options.MaxConcurrent = defaultMaxConcurrent
	}
	if options.UploadTTL <= 0 {
		options.UploadTTL = 24 * time.Hour
	}
	if options.ArtifactTTL <= 0 {
		options.ArtifactTTL = time.Hour
	}
}

func (s *Server) routes() {
	s.mux.HandleFunc("/health", exactPath("/health", s.health))
	s.mux.HandleFunc("/version", exactPath("/version", s.version))
	s.mux.HandleFunc("/status", exactPath("/status", s.status))
	s.mux.HandleFunc("/mcp", exactPath("/mcp", s.mcp))
	s.mux.HandleFunc("/sse", exactPath("/sse", s.legacySSE))
	s.mux.HandleFunc("/messages", exactPath("/messages", s.legacyMessages))
	s.mux.HandleFunc("/transfer/upload", exactPath("/transfer/upload", s.transfers.handleCreateUpload))
	s.mux.HandleFunc("/transfer/upload/", s.transfers.handleUpload)
	s.mux.HandleFunc("/transfer/download/", s.transfers.handleDownload)
	// Compatibility aliases never appear in newly generated URLs.
	s.mux.HandleFunc("/transfer/uploads", exactPath("/transfer/uploads", s.transfers.handleCreateUpload))
	s.mux.HandleFunc("/transfer/uploads/", s.transfers.handleUpload)
	s.mux.HandleFunc("/transfer/files/", s.transfers.handleDownload)
	s.mux.Handle("/", webui.New())
}

func exactPath(path string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		handler(w, r)
	}
}

// ServeHTTP applies the public security boundary before endpoint routing.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w.Header())
	if r.Method == http.MethodOptions {
		w.Header().Set("Allow", "GET, HEAD, POST, PUT, DELETE")
		writeAPIError(w, http.StatusMethodNotAllowed, "CORS_DISABLED", "不提供跨域访问")
		return
	}
	if err := s.access.validate(r); err != nil {
		writeAPIError(w, http.StatusForbidden, "NETWORK_POLICY_DENIED", err.Error())
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		w.Header().Set("Retry-After", "1")
		writeAPIError(w, http.StatusTooManyRequests, "CONCURRENCY_LIMIT", "并发连接已达到上限")
		return
	}
	if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
		limit := s.options.MaxRequestBytes
		if r.Method == http.MethodPut &&
			(strings.HasPrefix(r.URL.Path, "/transfer/upload/") ||
				strings.HasPrefix(r.URL.Path, "/transfer/uploads/")) {
			limit = s.options.TransferChunkBytes
		}
		if r.ContentLength > limit {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "请求体超过大小限制")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
	}
	s.mux.ServeHTTP(w, r)
}

func setSecurityHeaders(header http.Header) {
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("X-Frame-Options", "DENY")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
	header.Set("X-ZCR521-Anonymous-Root-Warning", "enabled")
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"service": "running",
	})
}

func (s *Server) version(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name":              "ZCR521 AI MCP",
		"author":            "小骨@Xiaogu_zcr521",
		"version":           s.options.Version,
		"protocolCurrent":   s.options.ProtocolCurrent,
		"protocolPrevious":  s.options.ProtocolPrevious,
		"protocolLegacy":    s.options.ProtocolLegacy,
		"streamableHTTP":    "/mcp",
		"legacySSE":         s.options.EnableLegacySSE,
		"legacySSEPath":     enabledPath(s.options.EnableLegacySSE, "/sse"),
		"legacyMessagePath": enabledPath(s.options.EnableLegacySSE, "/messages"),
	})
}

func enabledPath(enabled bool, path string) any {
	if !enabled {
		return nil
	}
	return path
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w, "GET, HEAD")
		return
	}
	status, err := s.options.Broker.Status(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "BROKER_UNAVAILABLE", err.Error())
		return
	}
	payload := cloneMap(status)
	payload["service"] = "running"
	payload["version"] = s.options.Version
	payload["protocolCurrent"] = s.options.ProtocolCurrent
	payload["protocolPrevious"] = s.options.ProtocolPrevious
	payload["protocolLegacy"] = s.options.ProtocolLegacy
	payload["mcpAddress"] = requestBaseURL(r) + "/mcp"
	payload["workDir"] = s.options.WorkDir
	payload["anonymousRootLAN"] = true
	payload["securityWarning"] = anonymousRootLANWarning
	if degraded, _ := payload["securityDegraded"].(bool); degraded {
		reason, _ := payload["securityDegradationReason"].(string)
		payload["securityWarning"] = anonymousRootLANWarning + " frontend 降权失败，当前以 Root 运行：" + reason
	}
	writeJSON(w, http.StatusOK, payload)
}

func cloneMap(source map[string]any) map[string]any {
	target := make(map[string]any, len(source)+8)
	for key, value := range source {
		target[key] = value
	}
	return target
}

func requestBaseURL(r *http.Request) string {
	return requestScheme(r) + "://" + r.Host
}

func (s *Server) mcp(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Vary", "MCP-Protocol-Version")
	handler, err := s.selectMCPHandler(r)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeAPIError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "MCP 请求体超过大小限制")
			return
		}
		writeAPIError(w, http.StatusBadRequest, "PROTOCOL_ROUTING_FAILED", err.Error())
		return
	}
	handler.ServeHTTP(w, r)
}

func (s *Server) selectMCPHandler(r *http.Request) (http.Handler, error) {
	version := strings.TrimSpace(r.Header.Get("MCP-Protocol-Version"))
	if version == s.options.ProtocolCurrent && version != "" {
		if s.options.MCP.Compatibility == nil || r.Method != http.MethodPost || r.Body == nil {
			return s.options.MCP.SDKCurrent, nil
		}
		return s.selectCurrentExtension(r)
	}
	if r.Method != http.MethodPost || r.Body == nil {
		return s.options.MCP.SDKStreamable, nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("读取 MCP 请求: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var envelope mcpRoutingEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		// The selected protocol handler owns JSON-RPC parse errors.
		return s.options.MCP.SDKStreamable, nil
	}
	currentClaim := envelope.claimsCurrentProtocol(s.options.ProtocolCurrent)
	if envelope.usesTaskCompatibility(currentClaim) && s.options.MCP.Compatibility != nil {
		return s.options.MCP.Compatibility, nil
	}
	if envelope.Method == "server/discover" ||
		currentClaim {
		return s.options.MCP.SDKCurrent, nil
	}
	return s.options.MCP.SDKStreamable, nil
}

func (s *Server) selectCurrentExtension(r *http.Request) (http.Handler, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, fmt.Errorf("读取 MCP 请求: %w", err)
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var envelope mcpRoutingEnvelope
	if json.Unmarshal(body, &envelope) == nil && envelope.usesTaskCompatibility(true) {
		return s.options.MCP.Compatibility, nil
	}
	return s.options.MCP.SDKCurrent, nil
}

type mcpRoutingEnvelope struct {
	Method string `json:"method"`
	Params struct {
		ProtocolVersion string         `json:"protocolVersion"`
		Name            string         `json:"name"`
		Arguments       map[string]any `json:"arguments"`
		Meta            map[string]any `json:"_meta"`
	} `json:"params"`
}

func (e mcpRoutingEnvelope) claimsCurrentProtocol(current string) bool {
	if e.Params.ProtocolVersion == current {
		return true
	}
	version, _ := e.Params.Meta["io.modelcontextprotocol/protocolVersion"].(string)
	if version == "" {
		// Compatibility with early release-candidate clients.
		version, _ = e.Params.Meta["protocolVersion"].(string)
	}
	return version == current
}

func (e mcpRoutingEnvelope) usesTaskCompatibility(current bool) bool {
	if !current {
		return false
	}
	if strings.HasPrefix(e.Method, "tasks/") {
		return true
	}
	if e.Method != "tools/call" {
		return false
	}
	background, _ := e.Params.Arguments["background"].(bool)
	if !background {
		return false
	}
	capabilities, _ := e.Params.Meta["io.modelcontextprotocol/clientCapabilities"].(map[string]any)
	extensions, _ := capabilities["extensions"].(map[string]any)
	_, declared := extensions["io.modelcontextprotocol/tasks"]
	return declared
}

func (s *Server) legacySSE(w http.ResponseWriter, r *http.Request) {
	if !s.options.EnableLegacySSE {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		methodNotAllowed(w, "GET")
		return
	}
	// The SDK derives the endpoint event from the request URL. Internally
	// presenting /messages makes the event point to the documented POST path
	// while the client still opens the public GET /sse endpoint.
	clone := r.Clone(r.Context())
	urlCopy := *r.URL
	urlCopy.Path = "/messages"
	clone.URL = &urlCopy
	s.options.MCP.LegacySSE.ServeHTTP(w, clone)
}

func (s *Server) legacyMessages(w http.ResponseWriter, r *http.Request) {
	if !s.options.EnableLegacySSE {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w, "POST")
		return
	}
	s.options.MCP.LegacyMessages.ServeHTTP(w, r)
}

func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "请求方法不受支持")
}

// PublishFile creates an unguessable, expiring download URL without exposing a
// device path. The source is streamed by the HTTP server and is never buffered
// in memory.
func (s *Server) PublishFile(path, name, sha256 string, ttl time.Duration) (TransferArtifact, error) {
	return s.transfers.PublishFile(path, name, sha256, ttl)
}

// Transfers exposes the transfer manager to the daemon integration layer.
func (s *Server) Transfers() *TransferManager {
	return s.transfers
}

// ListenAddress returns the configured address without starting a listener.
func (s *Server) ListenAddress() string {
	return net.JoinHostPort(s.options.ListenAddr, strconv.Itoa(s.options.Port))
}
