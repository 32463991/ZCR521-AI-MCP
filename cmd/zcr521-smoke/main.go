// Command zcr521-smoke performs a non-destructive end-to-end acceptance check
// against a running ZCR521 MCP endpoint. It exercises the real HTTP transport,
// frontend, broker and operation result envelope rather than calling handlers
// in-process.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/zcr521/android-ai-mcp/internal/buildinfo"
	"github.com/zcr521/android-ai-mcp/internal/model"
)

type probe struct {
	name        string
	arguments   map[string]any
	mustSucceed bool
}

type probeResult struct {
	Name     string `json:"name"`
	Success  bool   `json:"success"`
	Code     string `json:"code"`
	IsError  bool   `json:"isError"`
	Strategy string `json:"strategy"`
}

type report struct {
	Endpoint             string        `json:"endpoint"`
	Protocol             string        `json:"protocol"`
	ToolsListed          int           `json:"toolsListed"`
	ToolsCalled          int           `json:"toolsCalled"`
	SuccessfulCalls      int           `json:"successfulCalls"`
	StructuredErrors     int           `json:"structuredErrors"`
	RegisteredOnly       []string      `json:"registeredOnly"`
	ConcurrentRequests   int           `json:"concurrentRequests"`
	ConcurrentWorkers    int           `json:"concurrentWorkers"`
	BackgroundTaskID     string        `json:"backgroundTaskId"`
	BackgroundTaskStatus string        `json:"backgroundTaskStatus"`
	Results              []probeResult `json:"results"`
	DurationMS           int64         `json:"durationMs"`
}

var requiredResultFields = []string{
	"success", "code", "message", "data", "error", "stdout", "stderr",
	"exitCode", "durationMs", "taskId", "rebootRequired", "artifacts", "strategy",
}

func main() {
	endpoint := flag.String("endpoint", "http://127.0.0.1:5322/mcp", "MCP Streamable HTTP endpoint")
	timeout := flag.Duration("timeout", 90*time.Second, "overall timeout")
	concurrency := flag.Int("concurrency", 16, "parallel status workers")
	requests := flag.Int("requests", 128, "parallel status requests")
	flag.Parse()

	if *concurrency < 1 || *concurrency > 128 || *requests < 1 || *requests > 10000 {
		exitError(errors.New("concurrency must be 1..128 and requests must be 1..10000"))
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	if err := checkHealth(ctx, strings.TrimSuffix(*endpoint, "/mcp")); err != nil {
		exitError(err)
	}
	client := mcp.NewClient(&mcp.Implementation{
		Name:    "zcr521-runtime-smoke",
		Version: buildinfo.Version,
	}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: *endpoint}, nil)
	if err != nil {
		exitError(fmt.Errorf("connect MCP: %w", err))
	}
	defer session.Close()

	protocol := session.InitializeResult().ProtocolVersion
	if protocol != buildinfo.ProtocolCurrent {
		exitError(fmt.Errorf("protocol=%q, want %q", protocol, buildinfo.ProtocolCurrent))
	}
	list, err := session.ListTools(ctx, nil)
	if err != nil {
		exitError(fmt.Errorf("tools/list: %w", err))
	}
	if len(list.Tools) != 48 {
		exitError(fmt.Errorf("tools/list returned %d tools, want 48", len(list.Tools)))
	}
	available := make(map[string]bool, len(list.Tools))
	for _, tool := range list.Tools {
		available[tool.Name] = true
	}

	runID := fmt.Sprintf("smoke-%d", time.Now().UTC().UnixNano())
	filePath := runID + "/probe.txt"
	archivePath := runID + "/probe.zip"
	probes := acceptanceProbes(filePath, archivePath)
	if len(probes) != 48 {
		exitError(fmt.Errorf("internal probe count=%d, want 48", len(probes)))
	}
	for _, item := range probes {
		if !available[item.name] {
			exitError(fmt.Errorf("required tool not listed: %s", item.name))
		}
	}

	output := report{
		Endpoint:          *endpoint,
		Protocol:          protocol,
		ToolsListed:       len(list.Tools),
		ConcurrentWorkers: *concurrency,
		Results:           make([]probeResult, 0, len(probes)),
		RegisteredOnly:    []string{},
	}
	for _, item := range probes {
		// Calling zcr521_power/reboot is safe on a non-Android host because the
		// platform guard runs first. Never turn a smoke check into a real reboot.
		if item.name == "zcr521_power" && runtime.GOOS == "android" {
			output.RegisteredOnly = append(output.RegisteredOnly, item.name)
			continue
		}
		envelope, isError, err := callTool(ctx, session, item.name, item.arguments)
		if err != nil {
			exitError(fmt.Errorf("%s: %w", item.name, err))
		}
		if item.mustSucceed && !envelope.Success {
			exitError(fmt.Errorf("%s should succeed: code=%s message=%s error=%v",
				item.name, envelope.Code, envelope.Message, envelope.Error))
		}
		if envelope.Code == "UNKNOWN_TOOL" || envelope.Code == "INTERNAL_ERROR" ||
			envelope.Strategy == "action_validation" {
			exitError(fmt.Errorf("%s reached an invalid dispatcher state: %+v", item.name, envelope))
		}
		if isError == envelope.Success {
			exitError(fmt.Errorf("%s MCP isError=%v disagrees with success=%v",
				item.name, isError, envelope.Success))
		}
		output.ToolsCalled++
		if envelope.Success {
			output.SuccessfulCalls++
		} else {
			output.StructuredErrors++
		}
		output.Results = append(output.Results, probeResult{
			Name: item.name, Success: envelope.Success, Code: envelope.Code,
			IsError: isError, Strategy: envelope.Strategy,
		})
	}

	if err := concurrentStatus(ctx, session, *concurrency, *requests); err != nil {
		exitError(err)
	}
	output.ConcurrentRequests = *requests

	taskID, status, err := cancelBackgroundShell(ctx, session)
	if err != nil {
		exitError(err)
	}
	output.BackgroundTaskID = taskID
	output.BackgroundTaskStatus = status
	output.DurationMS = time.Since(started).Milliseconds()
	sort.Slice(output.Results, func(i, j int) bool {
		return output.Results[i].Name < output.Results[j].Name
	})
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(output); err != nil {
		exitError(err)
	}
}

func acceptanceProbes(filePath, archivePath string) []probe {
	return []probe{
		{"zcr521_fs_write", map[string]any{"action": "create", "path": filePath, "content": "zcr521 runtime smoke\n", "createParents": true}, true},
		{"zcr521_fs_info", map[string]any{"action": "stat", "path": filePath}, true},
		{"zcr521_fs_read", map[string]any{"action": "text", "path": filePath}, true},
		{"zcr521_fs_manage", map[string]any{"action": "mkdir", "path": strings.TrimSuffix(filePath, "/probe.txt") + "/dir", "parents": true}, true},
		{"zcr521_fs_search", map[string]any{"action": "name", "path": strings.TrimSuffix(filePath, "/probe.txt"), "name": "*.txt"}, true},
		{"zcr521_fs_hash", map[string]any{"action": "calculate", "path": filePath, "algorithm": "sha256"}, true},
		{"zcr521_archive", map[string]any{"action": "create", "format": "zip", "destination": archivePath, "sources": []string{filePath}}, true},
		{"zcr521_transfer_export", map[string]any{"action": "file", "path": filePath}, true},
		{"zcr521_transfer_upload", map[string]any{"action": "status", "path": strings.TrimSuffix(filePath, "probe.txt") + "upload.bin"}, true},
		{"zcr521_shell", map[string]any{"action": "exec", "command": smokeEchoCommand(), "identity": "current"}, true},
		{"zcr521_status", map[string]any{"action": "get"}, true},
		{"zcr521_capabilities", map[string]any{"action": "get"}, true},
		{"zcr521_config", map[string]any{"action": "get"}, true},
		{"zcr521_task", map[string]any{"action": "list"}, true},
		{"zcr521_schedule", map[string]any{"action": "list"}, true},
		{"zcr521_backup", map[string]any{"action": "list"}, true},
		{"zcr521_network", map[string]any{"action": "interfaces"}, true},
		{"zcr521_diagnostics", map[string]any{"action": "self_test"}, false},
		{"zcr521_download", map[string]any{"action": "status", "taskId": "zcr521-smoke-missing"}, false},
		{"zcr521_script", map[string]any{"action": "validate", "script": "echo zcr521-smoke"}, false},
		{"zcr521_process", map[string]any{"action": "list"}, false},
		{"zcr521_accessibility", map[string]any{"action": "list"}, false},
		{"zcr521_app_export", map[string]any{"action": "apk"}, false},
		{"zcr521_app_info", map[string]any{"action": "get"}, false},
		{"zcr521_app_install", map[string]any{"action": "apk"}, false},
		{"zcr521_app_list", map[string]any{"action": "list"}, false},
		{"zcr521_app_manage", map[string]any{"action": "launch"}, false},
		{"zcr521_app_permission", map[string]any{"action": "list"}, false},
		{"zcr521_app_policy", map[string]any{"action": "get"}, false},
		{"zcr521_audio", map[string]any{"action": "get"}, false},
		{"zcr521_connectivity", map[string]any{"action": "get"}, false},
		{"zcr521_default_app", map[string]any{"action": "get"}, false},
		{"zcr521_developer", map[string]any{"action": "get"}, false},
		{"zcr521_device_info", map[string]any{"action": "get"}, false},
		{"zcr521_display", map[string]any{"action": "get"}, false},
		{"zcr521_input", map[string]any{"action": "tap"}, false},
		{"zcr521_input_method", map[string]any{"action": "list"}, false},
		{"zcr521_locale_time", map[string]any{"action": "get"}, false},
		{"zcr521_log", map[string]any{"action": "mcp"}, false},
		{"zcr521_notification", map[string]any{"action": "get"}, false},
		{"zcr521_power", map[string]any{"action": "reboot", "confirmDangerous": false}, false},
		{"zcr521_property", map[string]any{"action": "list"}, false},
		{"zcr521_root_info", map[string]any{"action": "detect"}, false},
		{"zcr521_root_module", map[string]any{"action": "list"}, false},
		{"zcr521_screen", map[string]any{"action": "foreground"}, false},
		{"zcr521_service", map[string]any{"action": "binder_list"}, false},
		{"zcr521_setting", map[string]any{"action": "list"}, false},
		{"zcr521_systemless", map[string]any{"action": "list"}, false},
	}
}

func smokeEchoCommand() string {
	if runtime.GOOS == "windows" {
		return "echo zcr521-runtime-smoke"
	}
	return "printf '%s\\n' zcr521-runtime-smoke"
}

func longCommand() string {
	if runtime.GOOS == "windows" {
		return "ping -n 31 127.0.0.1 >NUL"
	}
	return "sleep 30"
}

func callTool(
	ctx context.Context,
	session *mcp.ClientSession,
	name string,
	arguments map[string]any,
) (model.Result, bool, error) {
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		return model.Result{}, false, err
	}
	var raw string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			raw = text.Text
			break
		}
	}
	if raw == "" {
		return model.Result{}, result.IsError, errors.New("tool result has no JSON text content")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return model.Result{}, result.IsError, fmt.Errorf("decode result object: %w", err)
	}
	for _, field := range requiredResultFields {
		if _, ok := fields[field]; !ok {
			return model.Result{}, result.IsError, fmt.Errorf("result is missing field %q", field)
		}
	}
	var envelope model.Result
	if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
		return model.Result{}, result.IsError, err
	}
	if envelope.Code == "" || strings.TrimSpace(envelope.Message) == "" {
		return model.Result{}, result.IsError, errors.New("result code/message is empty")
	}
	return envelope, result.IsError, nil
}

func concurrentStatus(
	ctx context.Context,
	session *mcp.ClientSession,
	workers int,
	requests int,
) error {
	jobs := make(chan struct{})
	var failed atomic.Bool
	var firstErr atomic.Pointer[string]
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range jobs {
				if failed.Load() {
					continue
				}
				result, _, err := callTool(ctx, session, "zcr521_status", map[string]any{"action": "get"})
				if err != nil || !result.Success {
					message := fmt.Sprintf("concurrent status failed: result=%+v err=%v", result, err)
					firstErr.CompareAndSwap(nil, &message)
					failed.Store(true)
				}
			}
		}()
	}
	for range requests {
		jobs <- struct{}{}
	}
	close(jobs)
	wg.Wait()
	if value := firstErr.Load(); value != nil {
		return errors.New(*value)
	}
	return nil
}

func cancelBackgroundShell(
	ctx context.Context,
	session *mcp.ClientSession,
) (string, string, error) {
	accepted, _, err := callTool(ctx, session, "zcr521_shell", map[string]any{
		"action": "exec", "command": longCommand(), "identity": "current", "background": true,
	})
	if err != nil {
		return "", "", fmt.Errorf("submit background shell: %w", err)
	}
	if !accepted.Success || accepted.TaskID == "" {
		return "", "", fmt.Errorf("background shell was not accepted: %+v", accepted)
	}
	cancelled, _, err := callTool(ctx, session, "zcr521_task", map[string]any{
		"action": "cancel", "taskId": accepted.TaskID,
	})
	if err != nil || !cancelled.Success {
		return accepted.TaskID, "", fmt.Errorf("cancel background shell: result=%+v err=%v", cancelled, err)
	}
	deadline := time.Now().Add(12 * time.Second)
	for {
		state, _, getErr := callTool(ctx, session, "zcr521_task", map[string]any{
			"action": "get", "taskId": accepted.TaskID,
		})
		if getErr != nil || !state.Success {
			return accepted.TaskID, "", fmt.Errorf("get cancelled task: result=%+v err=%v", state, getErr)
		}
		raw, _ := json.Marshal(state.Data)
		var task struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(raw, &task)
		switch task.Status {
		case "cancelled", "failed", "timed_out":
			return accepted.TaskID, task.Status, nil
		case "succeeded":
			return accepted.TaskID, task.Status, errors.New("long-running task completed before cancellation")
		}
		if time.Now().After(deadline) {
			return accepted.TaskID, task.Status, errors.New("cancelled task did not reach a terminal state")
		}
		select {
		case <-ctx.Done():
			return accepted.TaskID, task.Status, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func checkHealth(ctx context.Context, base string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("health request: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health HTTP status=%d", response.StatusCode)
	}
	return nil
}

func exitError(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "zcr521-smoke:", err)
	os.Exit(1)
}
