package mcpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zcr521/android-ai-mcp/internal/buildinfo"
	"github.com/zcr521/android-ai-mcp/internal/model"
)

type fakeTasks struct {
	state     any
	getErr    error
	updateErr error
	cancelErr error
}

func (f fakeTasks) TaskGet(context.Context, string) (any, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.state != nil {
		return f.state, nil
	}
	return completedFakeTask(), nil
}

func (fakeTasks) TaskList(context.Context) (any, error) {
	return []any{}, nil
}

func (f fakeTasks) TaskUpdate(context.Context, string, float64, string) (any, error) {
	return f.state, f.updateErr
}

func (f fakeTasks) TaskCancel(context.Context, string) (any, error) {
	return f.state, f.cancelErr
}

type fakeTaskCaller struct {
	result model.Result
}

func (f fakeTaskCaller) Call(context.Context, string, map[string]any) model.Result {
	return f.result
}

func TestTasksGetReturnsCurrentExtensionShape(t *testing.T) {
	handler := TasksHandler{Client: fakeTasks{}}
	request := currentTaskRequest(t, "tasks/get", map[string]any{"taskId": "task-x"}, "task-x")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	result, _ := envelope["result"].(map[string]any)
	if result["resultType"] != "complete" ||
		result["taskId"] != "task-x" ||
		result["status"] != "completed" {
		t.Fatalf("unexpected task result: %#v", result)
	}
	final, _ := result["result"].(map[string]any)
	if isError, _ := final["isError"].(bool); isError {
		t.Fatalf("unexpected final result: %#v", final)
	}
}

func TestTaskMethodsRequirePerRequestCapability(t *testing.T) {
	handler := TasksHandler{Client: fakeTasks{}}
	for _, method := range []string{"tasks/get", "tasks/update", "tasks/cancel"} {
		t.Run(method, func(t *testing.T) {
			params := map[string]any{
				"taskId": "task-x",
				"_meta": map[string]any{
					metaProtocolVersion: buildinfo.ProtocolCurrent,
					metaClientCapabilities: map[string]any{
						"extensions": map[string]any{},
					},
				},
			}
			if method == "tasks/update" {
				params["inputResponses"] = map[string]any{}
			}
			request := rawTaskRequest(t, method, params, "task-x")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest ||
				!strings.Contains(response.Body.String(), `-32021`) ||
				!strings.Contains(response.Body.String(), tasksExtensionID) {
				t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestUnknownTaskIDUsesInvalidParams(t *testing.T) {
	handler := TasksHandler{Client: fakeTasks{getErr: os.ErrNotExist}}
	request := currentTaskRequest(t, "tasks/get", map[string]any{"taskId": "unknown"}, "unknown")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `-32602`) {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
}

func TestRemovedTasksListUsesMethodNotFound(t *testing.T) {
	handler := TasksHandler{Client: fakeTasks{}}
	request := currentTaskRequest(t, "tasks/list", map[string]any{}, "")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if !strings.Contains(response.Body.String(), `-32601`) {
		t.Fatalf("missing method error: %s", response.Body.String())
	}
}

func TestTaskRoutingHeadersAreValidated(t *testing.T) {
	handler := TasksHandler{Client: fakeTasks{}}
	request := currentTaskRequest(t, "tasks/get", map[string]any{"taskId": "task-x"}, "wrong")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `-32020`) {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
}

func TestTaskUpdateAndCancelReturnEmptyAcknowledgement(t *testing.T) {
	handler := TasksHandler{Client: fakeTasks{}}
	tests := []struct {
		method string
		params map[string]any
	}{
		{
			method: "tasks/update",
			params: map[string]any{
				"taskId":         "task-x",
				"inputResponses": map[string]any{"unused": map[string]any{}},
			},
		},
		{
			method: "tasks/cancel",
			params: map[string]any{"taskId": "task-x"},
		},
	}
	for _, test := range tests {
		t.Run(test.method, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(
				response,
				currentTaskRequest(t, test.method, test.params, "task-x"),
			)
			if response.Code != http.StatusOK ||
				!strings.Contains(response.Body.String(), `"resultType":"complete"`) {
				t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestBackgroundToolCallReturnsDurableTaskHandle(t *testing.T) {
	accepted := model.Success("TASK_ACCEPTED", "accepted", map[string]any{
		"taskId": "task-x",
		"status": "queued",
	})
	accepted.TaskID = "task-x"
	handler := TasksHandler{
		Client: fakeTasks{state: map[string]any{
			"id":        "task-x",
			"status":    "queued",
			"message":   "waiting",
			"createdAt": "2026-07-30T00:00:00Z",
			"updatedAt": "2026-07-30T00:00:00Z",
		}},
		Caller: fakeTaskCaller{result: accepted},
	}
	params := map[string]any{
		"name": "zcr521_fs_hash",
		"arguments": map[string]any{
			"action":     "calculate",
			"path":       "sample.txt",
			"background": true,
		},
	}
	request := currentTaskRequest(t, "tools/call", params, "zcr521_fs_hash")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"resultType":"task"`) ||
		!strings.Contains(response.Body.String(), `"taskId":"task-x"`) {
		t.Fatalf("unexpected response: %d %s", response.Code, response.Body.String())
	}
}

func TestLegacyTaskMethodIsNotExposed(t *testing.T) {
	handler := TasksHandler{Client: fakeTasks{}}
	request := httptest.NewRequest(
		http.MethodPost,
		"/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tasks/get","params":{"taskId":"x"}}`),
	)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if !strings.Contains(response.Body.String(), `-32601`) {
		t.Fatalf("missing method error: %s", response.Body.String())
	}
}

func TestInternalTaskFailureIsCompletedToolError(t *testing.T) {
	failure := model.Failure("IO_ERROR", "failed", "PathError", "disk full")
	state := map[string]any{
		"id":        "task-x",
		"status":    "failed",
		"message":   "failed",
		"createdAt": "2026-07-30T00:00:00Z",
		"updatedAt": "2026-07-30T00:00:01Z",
		"result":    failure,
	}
	result, err := taskResult(state, 500)
	if err != nil {
		t.Fatal(err)
	}
	if result["status"] != "completed" {
		t.Fatalf("status = %#v", result["status"])
	}
	final, _ := result["result"].(map[string]any)
	if final["isError"] != true {
		t.Fatalf("final result = %#v", final)
	}
}

func TestTaskBackendErrorRemainsInternalError(t *testing.T) {
	handler := TasksHandler{Client: fakeTasks{getErr: errors.New("backend unavailable")}}
	request := currentTaskRequest(t, "tasks/get", map[string]any{"taskId": "task-x"}, "task-x")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if !strings.Contains(response.Body.String(), `-32603`) {
		t.Fatalf("unexpected response: %s", response.Body.String())
	}
}

func completedFakeTask() map[string]any {
	success := model.Success("OK", "done", map[string]any{"value": 1})
	return map[string]any{
		"id":        "task-x",
		"status":    "succeeded",
		"message":   "done",
		"createdAt": "2026-07-30T00:00:00Z",
		"updatedAt": "2026-07-30T00:00:01Z",
		"result":    success,
	}
}

func currentTaskRequest(
	t *testing.T,
	method string,
	params map[string]any,
	name string,
) *http.Request {
	t.Helper()
	meta, _ := params["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	meta[metaProtocolVersion] = buildinfo.ProtocolCurrent
	meta[metaClientCapabilities] = map[string]any{
		"extensions": map[string]any{
			tasksExtensionID: map[string]any{},
		},
	}
	params["_meta"] = meta
	return rawTaskRequest(t, method, params, name)
}

func rawTaskRequest(
	t *testing.T,
	method string,
	params map[string]any,
	name string,
) *http.Request {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(string(body)))
	request.Header.Set("MCP-Protocol-Version", buildinfo.ProtocolCurrent)
	request.Header.Set("Mcp-Method", method)
	if name != "" {
		request.Header.Set("Mcp-Name", name)
	}
	return request
}

func TestPollMillisecondsUsesConfiguredInterval(t *testing.T) {
	handler := TasksHandler{PollInterval: 2 * time.Second}
	if got := handler.pollMilliseconds(); got != 2000 {
		t.Fatalf("poll interval = %d", got)
	}
}

func TestTaskStreamRequiresExplicitSSEOnlyAccept(t *testing.T) {
	handler := TasksHandler{}
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request.Header.Set("Accept", "application/json, text/event-stream")
	if handler.wantsStream(request) {
		t.Fatal("mixed Accept must retain immediate JSON polling semantics")
	}
	request.Header.Set("Accept", "text/event-stream")
	if !handler.wantsStream(request) {
		t.Fatal("explicit SSE Accept must enable the compatibility progress stream")
	}
}
