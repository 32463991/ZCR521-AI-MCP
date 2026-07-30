package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zcr521/android-ai-mcp/internal/broker"
	"github.com/zcr521/android-ai-mcp/internal/buildinfo"
	"github.com/zcr521/android-ai-mcp/internal/config"
	"github.com/zcr521/android-ai-mcp/internal/frontend"
	"github.com/zcr521/android-ai-mcp/internal/mcpapi"
	"github.com/zcr521/android-ai-mcp/internal/model"
	"github.com/zcr521/android-ai-mcp/internal/service"
)

func TestEndToEndBrokerMCPTasksAndTransfer(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.Network.ListenLAN = false
	cfg.Paths.StateDir = filepath.Join(root, "state")
	cfg.Paths.WorkDir = filepath.Join(root, "work")
	cfg.Paths.DownloadsDir = filepath.Join(cfg.Paths.WorkDir, "downloads")
	cfg.Paths.UploadsDir = filepath.Join(cfg.Paths.WorkDir, "uploads")
	cfg.Paths.ArtifactsDir = filepath.Join(cfg.Paths.WorkDir, "output")
	cfg.Paths.TempDir = filepath.Join(cfg.Paths.StateDir, "tmp")
	cfg.Limits.MaxRequestBytes = 8 << 20
	cfg.Limits.TransferChunkBytes = 4 << 20
	if err := config.EnsureDirectories(cfg); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(cfg.Paths.StateDir, "config.json")
	if err := config.Save(configPath, cfg); err != nil {
		t.Fatal(err)
	}

	backend, err := service.New(configPath, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	socket := filepath.Join(root, "broker.sock")
	pidFile := filepath.Join(root, "frontend.pid")
	if err := os.WriteFile(pidFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	brokerServer, err := broker.NewServer(broker.ServerOptions{
		SocketPath:             socket,
		FrontendPIDFile:        pidFile,
		AllowedUIDs:            []uint32{0, 2000},
		AllowUnverifiedForHost: true,
	}, backend)
	if err != nil {
		t.Fatal(err)
	}
	brokerContext, stopBroker := context.WithCancel(context.Background())
	brokerDone := make(chan error, 1)
	go func() { brokerDone <- brokerServer.ListenAndServe(brokerContext) }()
	defer func() {
		stopBroker()
		select {
		case err := <-brokerDone:
			if err != nil {
				t.Errorf("broker shutdown: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("broker did not stop")
		}
	}()
	waitPath(t, socket)

	rootClient := broker.Client{SocketPath: socket}
	api, err := mcpapi.New(rootClient)
	if err != nil {
		t.Fatal(err)
	}
	taskHandler := mcpapi.TasksHandler{Client: rootClient, Caller: rootClient}
	handlers := frontend.NewOfficialMCPHandlers(
		func(*http.Request) *mcp.Server { return api.Server() },
		frontend.OfficialMCPOptions{Compatibility: taskHandler, MaxRequestBodyBytes: 8 << 20},
	)
	public, err := frontend.New(frontend.Options{
		Broker:             testFrontendBroker{client: rootClient},
		MCP:                handlers,
		Version:            buildinfo.Version,
		ProtocolCurrent:    buildinfo.ProtocolCurrent,
		ProtocolPrevious:   buildinfo.ProtocolPrevious,
		ProtocolLegacy:     buildinfo.ProtocolLegacySSE,
		ListenAddr:         "127.0.0.1",
		Port:               5322,
		WorkDir:            cfg.Paths.WorkDir,
		TransferDir:        filepath.Join(root, "transfer"),
		EnableLegacySSE:    true,
		MaxRequestBytes:    8 << 20,
		MaxUploadBytes:     32 << 20,
		TransferChunkBytes: 4 << 20,
		MaxConcurrent:      8,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(public)
	defer httpServer.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "zcr521-e2e", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if got := session.InitializeResult().ProtocolVersion; got != buildinfo.ProtocolCurrent {
		t.Fatalf("protocol = %s, want %s", got, buildinfo.ProtocolCurrent)
	}
	toolList, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(toolList.Tools) != 48 {
		t.Fatalf("tools/list returned %d tools", len(toolList.Tools))
	}
	writeResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "zcr521_fs_write",
		Arguments: map[string]any{
			"action":  "create",
			"path":    "e2e/hello.txt",
			"content": "hello MCP",
		},
	})
	if err != nil || writeResult.IsError {
		t.Fatalf("write: err=%v result=%#v", err, writeResult)
	}
	readResult, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "zcr521_fs_read",
		Arguments: map[string]any{
			"action": "text",
			"path":   "e2e/hello.txt",
		},
	})
	if err != nil || readResult.IsError || !strings.Contains(textOf(readResult), "hello MCP") {
		t.Fatalf("read: err=%v result=%s", err, textOf(readResult))
	}
	background, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name: "zcr521_fs_hash",
		Arguments: map[string]any{
			"action":     "calculate",
			"path":       "e2e/hello.txt",
			"algorithm":  "sha256",
			"background": true,
		},
	})
	if err != nil || background.IsError {
		t.Fatalf("background task: err=%v result=%#v", err, background)
	}
	var accepted model.Result
	if err := json.Unmarshal([]byte(textOf(background)), &accepted); err != nil || accepted.TaskID == "" {
		t.Fatalf("missing task id: %v %s", err, textOf(background))
	}
	waitTask(t, session, accepted.TaskID)
	testTaskExtension(t, httpServer.URL)

	testTransfer(t, httpServer.URL)
	testSecurityDenials(t, httpServer.URL)
}

func testTaskExtension(t *testing.T, baseURL string) {
	t.Helper()
	callBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      101,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "zcr521_fs_hash",
			"arguments": map[string]any{
				"action":     "calculate",
				"path":       "e2e/hello.txt",
				"algorithm":  "sha256",
				"background": true,
			},
			"_meta": taskMeta(true),
		},
	}
	callResponse := taskHTTPCall(t, baseURL, callBody, "tools/call", "zcr521_fs_hash")
	if callResponse.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(callResponse.Body)
		_ = callResponse.Body.Close()
		t.Fatalf("task-aware tools/call = %d: %s", callResponse.StatusCode, raw)
	}
	var created struct {
		Result struct {
			ResultType string `json:"resultType"`
			TaskID     string `json:"taskId"`
		} `json:"result"`
	}
	decodeAndClose(t, callResponse, &created)
	if created.Result.ResultType != "task" || created.Result.TaskID == "" {
		t.Fatalf("invalid CreateTaskResult: %#v", created)
	}

	getBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      102,
		"method":  "tasks/get",
		"params": map[string]any{
			"taskId": created.Result.TaskID,
			"_meta":  taskMeta(true),
		},
	}
	getResponse := taskHTTPCall(t, baseURL, getBody, "tasks/get", created.Result.TaskID)
	var got struct {
		Result struct {
			ResultType string `json:"resultType"`
			TaskID     string `json:"taskId"`
			Status     string `json:"status"`
		} `json:"result"`
	}
	decodeAndClose(t, getResponse, &got)
	if got.Result.ResultType != "complete" ||
		got.Result.TaskID != created.Result.TaskID ||
		(got.Result.Status != "working" && got.Result.Status != "completed") {
		t.Fatalf("invalid tasks/get result: %#v", got)
	}

	unknownBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      103,
		"method":  "tasks/get",
		"params": map[string]any{
			"taskId": "unknown-task-id",
			"_meta":  taskMeta(true),
		},
	}
	unknownResponse := taskHTTPCall(t, baseURL, unknownBody, "tasks/get", "unknown-task-id")
	defer unknownResponse.Body.Close()
	var unknown struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(unknownResponse.Body).Decode(&unknown); err != nil {
		t.Fatal(err)
	}
	if unknownResponse.StatusCode != http.StatusOK || unknown.Error.Code != -32602 {
		t.Fatalf("unknown task = HTTP %d, RPC %d", unknownResponse.StatusCode, unknown.Error.Code)
	}

	missingCapabilityBody := map[string]any{
		"jsonrpc": "2.0",
		"id":      104,
		"method":  "tasks/get",
		"params": map[string]any{
			"taskId": created.Result.TaskID,
			"_meta":  taskMeta(false),
		},
	}
	missingResponse := taskHTTPCall(
		t,
		baseURL,
		missingCapabilityBody,
		"tasks/get",
		created.Result.TaskID,
	)
	defer missingResponse.Body.Close()
	var missing struct {
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(missingResponse.Body).Decode(&missing); err != nil {
		t.Fatal(err)
	}
	if missingResponse.StatusCode != http.StatusBadRequest || missing.Error.Code != -32021 {
		t.Fatalf(
			"missing task capability = HTTP %d, RPC %d",
			missingResponse.StatusCode,
			missing.Error.Code,
		)
	}
}

func taskMeta(withTasks bool) map[string]any {
	extensions := map[string]any{}
	if withTasks {
		extensions["io.modelcontextprotocol/tasks"] = map[string]any{}
	}
	return map[string]any{
		"io.modelcontextprotocol/protocolVersion": buildinfo.ProtocolCurrent,
		"io.modelcontextprotocol/clientInfo": map[string]any{
			"name":    "zcr521-e2e",
			"version": "1",
		},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{
			"extensions": extensions,
		},
	}
}

func taskHTTPCall(
	t *testing.T,
	baseURL string,
	body map[string]any,
	method string,
	name string,
) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(
		http.MethodPost,
		baseURL+"/mcp",
		bytes.NewReader(raw),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("MCP-Protocol-Version", buildinfo.ProtocolCurrent)
	request.Header.Set("Mcp-Method", method)
	request.Header.Set("Mcp-Name", name)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func testTransfer(t *testing.T, baseURL string) {
	t.Helper()
	content := []byte("0123456789")
	hash := sha256.Sum256(content)
	body := fmt.Sprintf(`{"name":"sample.bin","size":%d,"sha256":"%s"}`, len(content), hex.EncodeToString(hash[:]))
	response, err := http.Post(baseURL+"/transfer/upload", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var created map[string]any
	decodeAndClose(t, response, &created)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("upload id missing: %#v", created)
	}
	request, _ := http.NewRequest(http.MethodPut, baseURL+"/transfer/upload/"+id, bytes.NewReader(content))
	request.Header.Set("Content-Range", "bytes 0-9/10")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		t.Fatalf("upload failed: %d %s", response.StatusCode, raw)
	}
	_ = response.Body.Close()
	response, err = http.Post(baseURL+"/transfer/upload/"+id+"/complete", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var completed map[string]any
	decodeAndClose(t, response, &completed)
	if completed["sha256"] != hex.EncodeToString(hash[:]) {
		t.Fatalf("upload checksum mismatch: %#v", completed)
	}
}

func testSecurityDenials(t *testing.T, baseURL string) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodGet, baseURL+"/status", nil)
	request.Header.Set("Origin", "https://attacker.invalid")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin status = %d", response.StatusCode)
	}
	request, _ = http.NewRequest(http.MethodGet, baseURL+"/status", nil)
	request.Host = "attacker.invalid"
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("invalid host status = %d", response.StatusCode)
	}
}

func waitTask(t *testing.T, session *mcp.ClientSession, id string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "zcr521_task",
			Arguments: map[string]any{
				"action": "get",
				"taskId": id,
			},
		})
		if err != nil || result.IsError {
			t.Fatalf("task get: %v %#v", err, result)
		}
		var envelope model.Result
		if err := json.Unmarshal([]byte(textOf(result)), &envelope); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(envelope.Data)
		var task struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(raw, &task)
		if task.Status == "succeeded" {
			return
		}
		if task.Status == "failed" || task.Status == "timed_out" || task.Status == "cancelled" {
			t.Fatalf("task ended as %s: %s", task.Status, raw)
		}
		if time.Now().After(deadline) {
			t.Fatalf("task timeout: %s", raw)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func textOf(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			return text.Text
		}
	}
	return ""
}

func decodeAndClose(t *testing.T, response *http.Response, destination any) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("HTTP %d: %s", response.StatusCode, raw)
	}
	if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
		t.Fatal(err)
	}
}

func waitPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("path not ready: %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type testFrontendBroker struct {
	client broker.Client
}

func (b testFrontendBroker) Call(ctx context.Context, tool string, args map[string]any) (frontend.Result, error) {
	value := b.client.Call(ctx, tool, args)
	artifacts := make([]any, len(value.Artifacts))
	for index := range value.Artifacts {
		artifacts[index] = value.Artifacts[index]
	}
	return frontend.Result{
		Success:        value.Success,
		Code:           value.Code,
		Message:        value.Message,
		Data:           value.Data,
		Error:          value.Error,
		Stdout:         value.Stdout,
		Stderr:         value.Stderr,
		ExitCode:       value.ExitCode,
		DurationMS:     value.DurationMS,
		TaskID:         value.TaskID,
		RebootRequired: value.RebootRequired,
		Artifacts:      artifacts,
		Strategy:       value.Strategy,
	}, nil
}

func (b testFrontendBroker) Status(ctx context.Context) (map[string]any, error) {
	return b.client.Status(ctx)
}
