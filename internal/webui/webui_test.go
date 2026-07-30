package webui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStatusPageIsSelfContainedReadOnlyAndWarns(t *testing.T) {
	handler := New()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:5322/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	body := response.Body.String()
	for _, required := range []string{
		"ZCR521 AI MCP",
		"匿名 Root 局域网访问已启用",
		`href="/assets/status.css"`,
		`src="/assets/status.js"`,
	} {
		if !strings.Contains(body, required) {
			t.Errorf("missing %q", required)
		}
	}
	if strings.Contains(body, "http://") || strings.Contains(body, "https://") {
		t.Fatal("status page references an external URL")
	}
	csp := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "form-action 'none'") {
		t.Fatalf("weak CSP %q", csp)
	}
}

func TestAssetsMeetMotionAndIdleRequirements(t *testing.T) {
	handler := New()
	read := func(path string) string {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1"+path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, response.Code)
		}
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	css := read("/assets/status.css")
	if !strings.Contains(css, "prefers-reduced-motion: reduce") {
		t.Fatal("missing reduced-motion override")
	}
	if strings.Contains(strings.ToLower(css), "gradient") {
		t.Fatal("gradients are not allowed")
	}
	js := read("/assets/status.js")
	if !strings.Contains(js, "document.hidden") ||
		!strings.Contains(js, `addEventListener("visibilitychange"`) {
		t.Fatal("hidden pages do not stop refreshing")
	}
	if strings.Contains(js, "innerHTML") {
		t.Fatal("status rendering must not use innerHTML")
	}
}
