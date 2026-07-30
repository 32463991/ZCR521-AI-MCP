package tasks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zcr521/android-ai-mcp/internal/model"
)

func TestSubmitPersistsAndCompletes(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "tasks"), 2, 1, func(ctx context.Context, task Task, reporter Reporter) model.Result {
		_ = reporter.Progress(0.5, "一半")
		_ = reporter.Log("stdout", "ok")
		return model.Success("OK", "完成", map[string]any{"tool": task.Tool})
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	task, err := manager.Submit("zcr521_fs_hash", map[string]any{"path": "x"}, true, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done, err := manager.Wait(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != StatusSucceeded || done.Result == nil || !done.Result.Success {
		t.Fatalf("unexpected result: %#v", done)
	}
	data, err := json.Marshal(done)
	if err != nil || len(data) == 0 {
		t.Fatal("task is not serializable")
	}
}

func TestDurableInterruptedTaskResumes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "tasks")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	task := Task{
		ID:        "00000000-0000-4000-8000-000000000001",
		Tool:      "zcr521_download",
		Arguments: json.RawMessage(`{"url":"x"}`),
		Status:    StatusRunning,
		Heavy:     true,
		Durable:   true,
		CreatedAt: now,
		StartedAt: now,
		UpdatedAt: now,
	}
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, task.ID+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	restored, err := New(dir, 1, 1, func(context.Context, Task, Reporter) model.Result {
		return model.Success("OK", "resumed", nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if count := restored.ResumeInterrupted(); count != 1 {
		t.Fatalf("expected one resumed task, got %d", count)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done, err := restored.Wait(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != StatusSucceeded {
		t.Fatalf("unexpected status: %s", done.Status)
	}
}

func TestTimeoutResultHasDistinctTerminalStatus(t *testing.T) {
	manager, err := New(filepath.Join(t.TempDir(), "tasks"), 1, 1, func(context.Context, Task, Reporter) model.Result {
		return model.Failure("TIMEOUT", "命令执行超时", "Timeout", "deadline exceeded")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	task, err := manager.Submit("zcr521_shell", map[string]any{"command": "sleep 10"}, false, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done, err := manager.Wait(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != StatusTimedOut || !done.Terminal() {
		t.Fatalf("timeout status = %q, terminal=%v", done.Status, done.Terminal())
	}
}
