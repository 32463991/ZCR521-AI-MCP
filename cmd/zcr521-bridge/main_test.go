package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func testBridgeConfig(t *testing.T, serverURL string) config {
	t.Helper()
	endpoint, err := validateEndpoint(serverURL + "/mcp")
	if err != nil {
		t.Fatal(err)
	}
	return config{
		endpoint:       endpoint,
		requestTimeout: 2 * time.Second,
		messageLimit:   64 << 10,
	}
}

func TestBridgeForwardsSessionProtocolAndDeletesSession(t *testing.T) {
	var mu sync.Mutex
	var posts int
	var deleted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Allow", "POST, DELETE")
			w.WriteHeader(http.StatusMethodNotAllowed)
		case http.MethodDelete:
			mu.Lock()
			deleted = r.Header.Get("Mcp-Session-Id") == "session-1"
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			var message struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			_ = json.Unmarshal(body, &message)
			mu.Lock()
			posts++
			call := posts
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if call == 1 {
				if r.Header.Get("Mcp-Session-Id") != "" {
					t.Error("initialize unexpectedly carried a session")
				}
				w.Header().Set("Mcp-Session-Id", "session-1")
				_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2026-07-28","capabilities":{},"serverInfo":{"name":"test","version":"1"}}}`)
				return
			}
			if got := r.Header.Get("Mcp-Session-Id"); got != "session-1" {
				t.Errorf("session header = %q", got)
			}
			if got := r.Header.Get("MCP-Protocol-Version"); got != "2026-07-28" {
				t.Errorf("protocol header = %q", got)
			}
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":2,"result":{}}`)
		}
	}))
	defer server.Close()

	input := strings.NewReader(
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"initialize\",\"params\":{\"protocolVersion\":\"2026-07-28\"}}\n" +
			"{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"ping\"}\n",
	)
	var output bytes.Buffer
	var logs bytes.Buffer
	runner := newBridge(context.Background(), testBridgeConfig(t, server.URL), input, &output, &logs)
	if err := runner.run(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("stdout lines = %d, want 2: %q; logs=%q", len(lines), output.String(), logs.String())
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Fatalf("stdout contains non-protocol data: %q", line)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !deleted {
		t.Fatal("bridge did not terminate the HTTP session")
	}
}

func TestBridgeConvertsPOSTSSEToStdio(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "id: event-1\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":7,\"result\":{\"ok\":true}}\n\n")
	}))
	defer server.Close()
	input := strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"initialize","params":{}}` + "\n")
	var output bytes.Buffer
	runner := newBridge(context.Background(), testBridgeConfig(t, server.URL), input, &output, io.Discard)
	if err := runner.run(); err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(output.String())
	if got != `{"jsonrpc":"2.0","id":7,"result":{"ok":true}}` {
		t.Fatalf("stdout = %q", got)
	}
	if runner.lastEventID() != "event-1" {
		t.Fatalf("last event id = %q", runner.lastEventID())
	}
}

func TestBridgeReturnsJSONRPCErrorWithOriginalID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	input := strings.NewReader(`{"jsonrpc":"2.0","id":"abc","method":"ping"}` + "\n")
	var output bytes.Buffer
	runner := newBridge(context.Background(), testBridgeConfig(t, server.URL), input, &output, io.Discard)
	if err := runner.run(); err != nil {
		t.Fatal(err)
	}
	var response struct {
		ID    string `json:"id"`
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(output.Bytes()), &response); err != nil {
		t.Fatal(err)
	}
	if response.ID != "abc" || response.Error.Code != -32098 {
		t.Fatalf("transport error response = %+v", response)
	}
}

func TestBridgeNotificationFailureStaysOffStdout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	input := strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n")
	var output bytes.Buffer
	var logs bytes.Buffer
	runner := newBridge(context.Background(), testBridgeConfig(t, server.URL), input, &output, &logs)
	if err := runner.run(); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatalf("notification failure polluted stdout: %q", output.String())
	}
	if !strings.Contains(logs.String(), "通知转发失败") {
		t.Fatalf("missing stderr diagnostic: %q", logs.String())
	}
}

func TestValidateEndpoint(t *testing.T) {
	tests := []struct {
		raw      string
		wantPath string
		wantErr  bool
	}{
		{"http://127.0.0.1:5322", "/mcp", false},
		{"https://phone.local/custom", "/custom", false},
		{"file:///tmp/mcp", "", true},
		{"http://user:pass@127.0.0.1/mcp", "", true},
		{"http://127.0.0.1/mcp?token=x", "", true},
	}
	for _, test := range tests {
		endpoint, err := validateEndpoint(test.raw)
		if (err != nil) != test.wantErr {
			t.Fatalf("validateEndpoint(%q) error = %v", test.raw, err)
		}
		if err == nil && endpoint.Path != test.wantPath {
			t.Fatalf("validateEndpoint(%q) path = %q, want %q", test.raw, endpoint.Path, test.wantPath)
		}
	}
}

func TestNewProtocolHeadersAreDerivedFromStdioMessage(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"zcr521_device","arguments":{},"_meta":{"protocolVersion":"2026-07-28"}}}`)
	info, err := inspectMessage(raw)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:5322/mcp", bytes.NewReader(raw))
	applyMessageHeaders(request, info)
	if got := request.Header.Get("MCP-Protocol-Version"); got != "2026-07-28" {
		t.Fatalf("protocol = %q", got)
	}
	if got := request.Header.Get("Mcp-Method"); got != "tools/call" {
		t.Fatalf("method = %q", got)
	}
	if got := request.Header.Get("Mcp-Name"); got != "zcr521_device" {
		t.Fatalf("name = %q", got)
	}
}
