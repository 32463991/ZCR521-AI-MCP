package mcpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/zcr521/android-ai-mcp/internal/buildinfo"
	"github.com/zcr521/android-ai-mcp/internal/model"
)

const (
	tasksExtensionID                    = "io.modelcontextprotocol/tasks"
	metaProtocolVersion                 = "io.modelcontextprotocol/protocolVersion"
	metaClientCapabilities              = "io.modelcontextprotocol/clientCapabilities"
	metaServerInfo                      = "io.modelcontextprotocol/serverInfo"
	codeHeaderMismatch                  = -32020
	codeMissingRequiredClientCapability = -32021
)

type TaskClient interface {
	TaskGet(context.Context, string) (any, error)
	TaskList(context.Context) (any, error)
	TaskUpdate(context.Context, string, float64, string) (any, error)
	TaskCancel(context.Context, string) (any, error)
}

type TasksHandler struct {
	Client       TaskClient
	Caller       Caller
	MaxBodyBytes int64
	PollInterval time.Duration
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (h TasksHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.Client == nil {
		h.writeError(w, nil, -32603, "任务后端未配置", nil, http.StatusInternalServerError)
		return
	}
	limit := h.MaxBodyBytes
	if limit <= 0 {
		limit = 1 << 20
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || int64(len(body)) > limit {
		h.writeError(w, nil, -32600, "请求体无效或过大", errorText(err), http.StatusBadRequest)
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		h.writeError(w, nil, -32600, "空 JSON-RPC 请求", nil, http.StatusBadRequest)
		return
	}
	if body[0] == '[' {
		h.serveBatch(w, r, body)
		return
	}
	var request rpcRequest
	if err := json.Unmarshal(body, &request); err != nil {
		h.writeError(w, nil, -32700, "JSON 解析失败", err.Error(), http.StatusBadRequest)
		return
	}
	if h.wantsStream(r) && request.Method == "tasks/get" {
		h.streamTask(w, r, request)
		return
	}
	response := h.dispatch(r.Context(), request, r.Header, true)
	if len(request.ID) == 0 || bytes.Equal(request.ID, []byte("null")) {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeRPCJSON(w, rpcHTTPStatus(response), response)
}

func (h TasksHandler) serveBatch(w http.ResponseWriter, r *http.Request, body []byte) {
	var requests []rpcRequest
	if err := json.Unmarshal(body, &requests); err != nil || len(requests) == 0 {
		h.writeError(w, nil, -32600, "无效 JSON-RPC 批请求", errorText(err), http.StatusBadRequest)
		return
	}
	responses := make([]rpcResponse, 0, len(requests))
	status := http.StatusOK
	for _, request := range requests {
		if len(request.ID) == 0 || bytes.Equal(request.ID, []byte("null")) {
			_ = h.dispatch(r.Context(), request, r.Header, false)
			continue
		}
		response := h.dispatch(r.Context(), request, r.Header, false)
		if rpcHTTPStatus(response) == http.StatusBadRequest {
			status = http.StatusBadRequest
		}
		responses = append(responses, response)
	}
	if len(responses) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeRPCJSON(w, status, responses)
}

func (h TasksHandler) dispatch(
	ctx context.Context,
	request rpcRequest,
	header http.Header,
	validateHeaders bool,
) rpcResponse {
	response := rpcResponse{JSONRPC: "2.0", ID: request.ID}
	if request.JSONRPC != "2.0" || request.Method == "" {
		response.Error = &rpcError{Code: -32600, Message: "无效 JSON-RPC 请求"}
		return response
	}
	params := map[string]any{}
	if len(request.Params) > 0 && !bytes.Equal(request.Params, []byte("null")) {
		decoder := json.NewDecoder(bytes.NewReader(request.Params))
		decoder.UseNumber()
		if err := decoder.Decode(&params); err != nil {
			response.Error = &rpcError{Code: -32602, Message: "任务参数无效", Data: err.Error()}
			return response
		}
	}
	if !isCurrentTaskRequest(header, params) {
		response.Error = &rpcError{Code: -32601, Message: "当前协议版本不提供此任务扩展方法"}
		return response
	}
	switch request.Method {
	case "tasks/get", "tasks/update", "tasks/cancel", "tools/call":
	default:
		response.Error = &rpcError{Code: -32601, Message: "未实现的任务扩展方法"}
		return response
	}
	if !hasTasksCapability(params) {
		response.Error = missingTasksCapabilityError()
		return response
	}

	taskID, _ := params["taskId"].(string)
	name := taskID
	if request.Method == "tools/call" {
		name, _ = params["name"].(string)
	}
	if validateHeaders {
		if routingError := validateTaskRoutingHeaders(header, request.Method, name); routingError != nil {
			response.Error = routingError
			return response
		}
	}

	var err error
	switch request.Method {
	case "tasks/get":
		if taskID == "" {
			err = errors.New("taskId is required")
			break
		}
		var state any
		state, err = h.Client.TaskGet(ctx, taskID)
		if err == nil {
			response.Result, err = taskResult(state, h.pollMilliseconds())
		}
	case "tasks/update":
		if taskID == "" {
			err = errors.New("taskId is required")
			break
		}
		if inputResponses, exists := params["inputResponses"]; exists {
			if _, ok := inputResponses.(map[string]any); !ok {
				err = errors.New("inputResponses must be an object")
				break
			}
			// ZCR521 tasks do not currently enter input_required. Unknown or
			// already-satisfied response keys are therefore ignored, while the
			// task ID itself is still checked.
			_, err = h.Client.TaskGet(ctx, taskID)
		} else {
			// Retain the documented progress/message compatibility shape for
			// existing ZCR521 clients.
			progress, progressErr := jsonNumber(params["progress"])
			if progressErr != nil {
				err = progressErr
				break
			}
			message, _ := params["message"].(string)
			_, err = h.Client.TaskUpdate(ctx, taskID, progress, message)
		}
		if err == nil {
			response.Result = completeResult(nil)
		}
	case "tasks/cancel":
		if taskID == "" {
			err = errors.New("taskId is required")
			break
		}
		_, err = h.Client.TaskCancel(ctx, taskID)
		if err == nil {
			response.Result = completeResult(nil)
		}
	case "tools/call":
		response.Result, err = h.callToolAsTask(ctx, params)
	}
	if err != nil {
		code := -32603
		lower := strings.ToLower(err.Error())
		if errors.Is(err, os.ErrNotExist) ||
			taskID == "" ||
			strings.Contains(lower, "required") ||
			strings.Contains(lower, "not found") ||
			strings.Contains(lower, "does not exist") ||
			strings.Contains(lower, "no such file") ||
			strings.Contains(lower, "不存在") {
			code = -32602
		}
		response.Error = &rpcError{Code: code, Message: "任务操作失败", Data: err.Error()}
	}
	return response
}

func (h TasksHandler) streamTask(w http.ResponseWriter, r *http.Request, request rpcRequest) {
	initial := h.dispatch(r.Context(), request, r.Header, true)
	if initial.Error != nil {
		writeRPCJSON(w, rpcHTTPStatus(initial), initial)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeRPCJSON(w, http.StatusOK, initial)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	current, _ := json.Marshal(initial)
	eventID := 1
	writeSSE(w, eventID, current)
	flusher.Flush()
	if taskTerminal(initial.Result) {
		return
	}
	previous := current
	interval := h.PollInterval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
		response := h.dispatch(r.Context(), request, r.Header, true)
		current, _ = json.Marshal(response)
		if bytes.Equal(current, previous) {
			continue
		}
		if response.Error != nil {
			eventID++
			writeSSE(w, eventID, current)
			flusher.Flush()
			return
		}
		params := notificationTask(response.Result)
		// notifications/tasks is the standardized extension method. The
		// /status suffix remains as a compatibility alias promised by the first release.
		for _, method := range []string{"notifications/tasks", "notifications/tasks/status"} {
			eventID++
			notification := map[string]any{
				"jsonrpc": "2.0",
				"method":  method,
				"params":  params,
			}
			raw, _ := json.Marshal(notification)
			writeSSE(w, eventID, raw)
		}
		flusher.Flush()
		previous = current
		if taskTerminal(response.Result) {
			return
		}
	}
}

func (h TasksHandler) wantsStream(r *http.Request) bool {
	accept := strings.ToLower(r.Header.Get("Accept"))
	// A normal current-protocol client advertises both media types. Keep
	// tasks/get as an immediate polling request in that case; the compatibility
	// progress stream is selected only when the client explicitly asks for SSE
	// without also asking for a JSON response.
	return strings.Contains(accept, "text/event-stream") &&
		!strings.Contains(accept, "application/json")
}

func (h TasksHandler) writeError(
	w http.ResponseWriter,
	id json.RawMessage,
	code int,
	message string,
	data any,
	status int,
) {
	writeRPCJSON(w, status, rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: message, Data: data},
	})
}

func writeRPCJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeSSE(w io.Writer, id int, data []byte) {
	_, _ = fmt.Fprintf(w, "id: %d\nevent: message\ndata: %s\n\n", id, data)
}

func taskTerminal(value any) bool {
	raw, err := json.Marshal(value)
	if err != nil {
		return true
	}
	var task struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(raw, &task) != nil {
		return true
	}
	switch task.Status {
	case "succeeded", "failed", "timed_out", "cancelled", "completed":
		return true
	default:
		return false
	}
}

func jsonNumber(value any) (float64, error) {
	switch typed := value.(type) {
	case json.Number:
		return typed.Float64()
	case float64:
		return typed, nil
	default:
		return 0, errors.New("progress 必须是数字")
	}
}

func errorText(err error) any {
	if err == nil {
		return nil
	}
	return err.Error()
}

func (h TasksHandler) callToolAsTask(ctx context.Context, params map[string]any) (any, error) {
	if h.Caller == nil {
		return nil, errors.New("task-aware tool caller is not configured")
	}
	name, _ := params["name"].(string)
	if name == "" {
		return nil, errors.New("name is required")
	}
	arguments, ok := params["arguments"].(map[string]any)
	if !ok {
		return nil, errors.New("arguments must be an object")
	}
	if background, _ := arguments["background"].(bool); !background {
		return nil, errors.New("task-aware tools/call requires background=true")
	}
	result := h.Caller.Call(ctx, name, arguments)
	if result.TaskID == "" {
		return toolCallResult(result), nil
	}
	state, err := h.Client.TaskGet(ctx, result.TaskID)
	if err != nil {
		return nil, fmt.Errorf("durable task was not readable after creation: %w", err)
	}
	created, err := taskResult(state, h.pollMilliseconds())
	if err != nil {
		return nil, err
	}
	created["resultType"] = "task"
	return created, nil
}

func (h TasksHandler) pollMilliseconds() int64 {
	interval := h.PollInterval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	return interval.Milliseconds()
}

func isCurrentTaskRequest(header http.Header, params map[string]any) bool {
	if strings.TrimSpace(header.Get("MCP-Protocol-Version")) == buildinfo.ProtocolCurrent {
		return true
	}
	meta, _ := params["_meta"].(map[string]any)
	if meta == nil {
		return false
	}
	version, _ := meta[metaProtocolVersion].(string)
	if version == "" {
		// Compatibility with early 2026-07-28 release-candidate clients.
		version, _ = meta["protocolVersion"].(string)
	}
	return version == buildinfo.ProtocolCurrent
}

func hasTasksCapability(params map[string]any) bool {
	meta, _ := params["_meta"].(map[string]any)
	capabilities, _ := meta[metaClientCapabilities].(map[string]any)
	extensions, _ := capabilities["extensions"].(map[string]any)
	value, exists := extensions[tasksExtensionID]
	if !exists {
		return false
	}
	_, valid := value.(map[string]any)
	return valid
}

func missingTasksCapabilityError() *rpcError {
	return &rpcError{
		Code:    codeMissingRequiredClientCapability,
		Message: "Missing required client capability",
		Data: map[string]any{
			"requiredCapabilities": map[string]any{
				"extensions": map[string]any{
					tasksExtensionID: map[string]any{},
				},
			},
		},
	}
}

func validateTaskRoutingHeaders(header http.Header, method, name string) *rpcError {
	headerMethod := strings.TrimSpace(header.Get("Mcp-Method"))
	if headerMethod == "" {
		return &rpcError{Code: codeHeaderMismatch, Message: "missing required Mcp-Method header"}
	}
	if headerMethod != method {
		return &rpcError{
			Code:    codeHeaderMismatch,
			Message: fmt.Sprintf("Mcp-Method header %q does not match method %q", headerMethod, method),
		}
	}
	if name == "" {
		return nil
	}
	headerName := strings.TrimSpace(header.Get("Mcp-Name"))
	if headerName == "" {
		return &rpcError{Code: codeHeaderMismatch, Message: "missing required Mcp-Name header"}
	}
	if headerName != name {
		return &rpcError{
			Code:    codeHeaderMismatch,
			Message: fmt.Sprintf("Mcp-Name header %q does not match name %q", headerName, name),
		}
	}
	return nil
}

func taskResult(value any, pollIntervalMS int64) (map[string]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode task state: %w", err)
	}
	var source map[string]any
	if err := json.Unmarshal(raw, &source); err != nil {
		return nil, fmt.Errorf("decode task state: %w", err)
	}
	taskID := stringField(source, "taskId", "id")
	if taskID == "" {
		return nil, errors.New("task state does not contain a task ID")
	}
	sourceStatus := stringField(source, "status")
	status, err := extensionTaskStatus(sourceStatus)
	if err != nil {
		return nil, err
	}
	if (sourceStatus == "failed" || sourceStatus == "timed_out") && source["result"] == nil {
		status = "failed"
	}
	result := completeResult(map[string]any{
		"taskId":         taskID,
		"status":         status,
		"createdAt":      stringField(source, "createdAt"),
		"lastUpdatedAt":  stringField(source, "lastUpdatedAt", "updatedAt"),
		"ttlMs":          nil,
		"pollIntervalMs": pollIntervalMS,
	})
	if message := stringField(source, "statusMessage", "message"); message != "" {
		result["statusMessage"] = message
	}
	if result["createdAt"] == "" || result["lastUpdatedAt"] == "" {
		return nil, errors.New("task state does not contain required timestamps")
	}
	switch status {
	case "completed":
		if taskOutput, exists := source["result"]; exists && taskOutput != nil {
			result["result"], err = taskOutputResult(taskOutput)
			if err != nil {
				return nil, err
			}
		} else {
			result["result"] = completeResult(map[string]any{})
		}
	case "failed":
		result["error"] = map[string]any{
			"code":    -32603,
			"message": stringField(source, "message", "statusMessage"),
		}
	case "input_required":
		if inputRequests, exists := source["inputRequests"]; exists {
			result["inputRequests"] = inputRequests
		} else {
			result["inputRequests"] = map[string]any{}
		}
	}
	return result, nil
}

func extensionTaskStatus(status string) (string, error) {
	switch status {
	case "queued", "running", "cancelling", "working":
		return "working", nil
	case "succeeded", "completed":
		return "completed", nil
	case "cancelled":
		return "cancelled", nil
	case "failed", "timed_out":
		// Android tool failures are valid CallToolResult values with isError=true,
		// not JSON-RPC failures.
		return "completed", nil
	case "interrupted":
		return "failed", nil
	case "input_required":
		return "input_required", nil
	default:
		return "", fmt.Errorf("unsupported task status %q", status)
	}
}

func taskOutputResult(value any) (map[string]any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result model.Result
	if err := json.Unmarshal(raw, &result); err == nil && result.Code != "" {
		return toolCallResult(result), nil
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	return completeResult(generic), nil
}

func toolCallResult(result model.Result) map[string]any {
	raw, _ := json.Marshal(toolResult(result))
	value := map[string]any{}
	_ = json.Unmarshal(raw, &value)
	return completeResult(value)
}

func completeResult(value map[string]any) map[string]any {
	if value == nil {
		value = map[string]any{}
	}
	value["resultType"] = "complete"
	meta, _ := value["_meta"].(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	meta[metaServerInfo] = map[string]any{
		"name":    "zcr521-android-mcp",
		"version": buildinfo.Version,
	}
	value["_meta"] = meta
	return value
}

func notificationTask(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return value
	}
	result := map[string]any{}
	if json.Unmarshal(raw, &result) != nil {
		return value
	}
	delete(result, "resultType")
	delete(result, "_meta")
	return result
}

func stringField(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := value[key].(string); ok {
			return text
		}
	}
	return ""
}

func rpcHTTPStatus(response rpcResponse) int {
	if response.Error != nil &&
		(response.Error.Code == codeMissingRequiredClientCapability ||
			response.Error.Code == codeHeaderMismatch) {
		return http.StatusBadRequest
	}
	return http.StatusOK
}
