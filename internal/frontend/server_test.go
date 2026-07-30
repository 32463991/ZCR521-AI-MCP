package frontend

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type stubBroker struct {
	status map[string]any
	err    error
}

func (b *stubBroker) Call(context.Context, string, map[string]any) (Result, error) {
	return Result{}, nil
}

func (b *stubBroker) Status(context.Context) (map[string]any, error) {
	return b.status, b.err
}

func markerHandler(marker string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Test-Handler", marker)
		w.Header().Set("X-Test-Body", string(body))
		w.WriteHeader(http.StatusOK)
	})
}

func testOptions(t *testing.T) Options {
	t.Helper()
	return Options{
		Broker: &stubBroker{status: map[string]any{
			"androidVersion": "16",
			"rootAvailable":  true,
			"rootFramework":  "test",
			"taskCount":      2,
			"uptimeSeconds":  65,
		}},
		MCP: MCPHandlers{
			SDKStreamable:  markerHandler("stateful"),
			SDKCurrent:     markerHandler("current"),
			Compatibility:  markerHandler("compatibility"),
			LegacySSE:      markerHandler("legacy-sse"),
			LegacyMessages: markerHandler("legacy-messages"),
		},
		Version:            "0.01",
		ProtocolCurrent:    "2026-07-28",
		ProtocolLegacy:     "2024-11-05",
		ListenAddr:         "127.0.0.1",
		Port:               5322,
		WorkDir:            "/storage/emulated/0/zcr521AI",
		TransferDir:        filepath.Join(t.TempDir(), "transfer"),
		EnableLegacySSE:    true,
		MaxRequestBytes:    64 << 10,
		MaxUploadBytes:     1 << 20,
		TransferChunkBytes: 64 << 10,
		MaxConcurrent:      8,
		UploadTTL:          time.Hour,
		ArtifactTTL:        time.Hour,
	}
}

func localRequest(method, target string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, target, body)
	request.Host = "127.0.0.1:5322"
	request.RemoteAddr = "127.0.0.1:41000"
	return request
}

func TestMCPVersionRouting(t *testing.T) {
	server, err := New(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		header  string
		body    string
		handler string
	}{
		{
			name:    "current header",
			header:  "2026-07-28",
			body:    `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
			handler: "current",
		},
		{
			name:    "legacy initialize",
			body:    `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25"}}`,
			handler: "stateful",
		},
		{
			name:    "sessionless discovery without header",
			body:    `{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"protocolVersion":"2026-07-28"}}}`,
			handler: "current",
		},
		{
			name:    "current tasks extension",
			header:  "2026-07-28",
			body:    `{"jsonrpc":"2.0","id":1,"method":"tasks/get","params":{"taskId":"x"}}`,
			handler: "compatibility",
		},
		{
			name:   "current background tool with tasks extension",
			header: "2026-07-28",
			body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"zcr521_fs_hash",` +
				`"arguments":{"action":"calculate","path":"x","background":true},` +
				`"_meta":{"io.modelcontextprotocol/clientCapabilities":{"extensions":` +
				`{"io.modelcontextprotocol/tasks":{}}}}}}`,
			handler: "compatibility",
		},
		{
			name:   "current background tool without tasks extension",
			header: "2026-07-28",
			body: `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"zcr521_fs_hash",` +
				`"arguments":{"action":"calculate","path":"x","background":true}}}`,
			handler: "current",
		},
		{
			name:    "legacy task method stays on legacy SDK",
			body:    `{"jsonrpc":"2.0","id":1,"method":"tasks/get","params":{"taskId":"x"}}`,
			handler: "stateful",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := localRequest(http.MethodPost, "http://127.0.0.1:5322/mcp", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json, text/event-stream")
			if test.header != "" {
				request.Header.Set("MCP-Protocol-Version", test.header)
			}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if got := response.Header().Get("X-Test-Handler"); got != test.handler {
				t.Fatalf("handler = %q, want %q; body=%s", got, test.handler, response.Body.String())
			}
			if got := response.Header().Get("X-Test-Body"); got != test.body {
				t.Fatalf("forwarded body = %q, want %q", got, test.body)
			}
		})
	}
}

func TestStatusHasProminentAnonymousRootWarning(t *testing.T) {
	server, err := New(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, localRequest(http.MethodGet, "http://127.0.0.1:5322/status", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var status map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status["anonymousRootLAN"] != true {
		t.Fatalf("anonymousRootLAN = %#v", status["anonymousRootLAN"])
	}
	warning, _ := status["securityWarning"].(string)
	if !strings.Contains(warning, "完整 Root 能力") || !strings.Contains(warning, "公网") {
		t.Fatalf("warning is not prominent: %q", warning)
	}
}

func TestNetworkBoundaryAndCORS(t *testing.T) {
	server, err := New(testOptions(t))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*http.Request)
		method string
		want   int
	}{
		{
			name: "off link",
			mutate: func(r *http.Request) {
				r.RemoteAddr = "203.0.113.9:4040"
			},
			method: http.MethodGet,
			want:   http.StatusForbidden,
		},
		{
			name: "invalid host",
			mutate: func(r *http.Request) {
				r.Host = "attacker.example:5322"
			},
			method: http.MethodGet,
			want:   http.StatusForbidden,
		},
		{
			name: "cross origin",
			mutate: func(r *http.Request) {
				r.Header.Set("Origin", "http://attacker.example")
			},
			method: http.MethodGet,
			want:   http.StatusForbidden,
		},
		{
			name:   "preflight disabled",
			mutate: func(*http.Request) {},
			method: http.MethodOptions,
			want:   http.StatusMethodNotAllowed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := localRequest(test.method, "http://127.0.0.1:5322/health", nil)
			test.mutate(request)
			response := httptest.NewRecorder()
			server.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d: %s", response.Code, test.want, response.Body.String())
			}
			if value := response.Header().Get("Access-Control-Allow-Origin"); value != "" {
				t.Fatalf("unexpected CORS header %q", value)
			}
		})
	}
}

func TestRequestSizeLimitPrecedesMCPHandler(t *testing.T) {
	options := testOptions(t)
	options.MaxRequestBytes = 64
	server, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("x"), 65)
	request := localRequest(http.MethodPost, "http://127.0.0.1:5322/mcp", bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", response.Code)
	}
	if response.Header().Get("X-Test-Handler") != "" {
		t.Fatal("oversized request reached MCP handler")
	}
}

func TestLegacySSEAdvertisesMessagesEndpoint(t *testing.T) {
	sdkServer := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	options := testOptions(t)
	options.MCP = NewOfficialMCPHandlers(func(*http.Request) *mcp.Server {
		return sdkServer
	}, OfficialMCPOptions{MaxRequestBodyBytes: options.MaxRequestBytes})
	server, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+"/sse", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Accept", "text/event-stream")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := io.LimitReader(response.Body, 2048)
	data := make([]byte, 2048)
	count, readErr := reader.Read(data)
	cancel()
	if readErr != nil && !errorsIsContext(readErr) {
		t.Fatal(readErr)
	}
	payload := string(data[:count])
	if !strings.Contains(payload, "event: endpoint") ||
		!strings.Contains(payload, "data: /messages?sessionid=") {
		t.Fatalf("unexpected SSE endpoint event: %q", payload)
	}
}

func errorsIsContext(err error) bool {
	return err == context.Canceled || err == context.DeadlineExceeded
}
