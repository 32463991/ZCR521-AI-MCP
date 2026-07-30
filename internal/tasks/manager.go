// Package tasks provides durable, cancellable background work with independent
// total and heavyweight concurrency limits.
package tasks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/zcr521/android-ai-mcp/internal/atomicfile"
	"github.com/zcr521/android-ai-mcp/internal/model"
)

type Status string

const (
	StatusQueued      Status = "queued"
	StatusRunning     Status = "running"
	StatusSucceeded   Status = "succeeded"
	StatusFailed      Status = "failed"
	StatusTimedOut    Status = "timed_out"
	StatusCancelling  Status = "cancelling"
	StatusCancelled   Status = "cancelled"
	StatusInterrupted Status = "interrupted"
)

type Task struct {
	ID          string          `json:"id"`
	Tool        string          `json:"tool"`
	Arguments   json.RawMessage `json:"arguments"`
	Status      Status          `json:"status"`
	Progress    float64         `json:"progress"`
	Message     string          `json:"message"`
	Heavy       bool            `json:"heavy"`
	Durable     bool            `json:"durable"`
	CreatedAt   time.Time       `json:"createdAt"`
	StartedAt   time.Time       `json:"startedAt"`
	UpdatedAt   time.Time       `json:"updatedAt"`
	CompletedAt time.Time       `json:"completedAt"`
	Result      *model.Result   `json:"result"`
}

func (t Task) Terminal() bool {
	return t.Status == StatusSucceeded || t.Status == StatusFailed ||
		t.Status == StatusTimedOut || t.Status == StatusCancelled
}

type Runner func(context.Context, Task, Reporter) model.Result

type Reporter interface {
	Progress(value float64, message string) error
	Log(stream, text string) error
}

type Manager struct {
	dir       string
	runner    Runner
	totalSem  chan struct{}
	heavySem  chan struct{}
	mu        sync.RWMutex
	tasks     map[string]*Task
	cancel    map[string]context.CancelFunc
	waiters   map[string][]chan struct{}
	closeOnce sync.Once
	closed    chan struct{}
}

func New(dir string, totalLimit, heavyLimit int, runner Runner) (*Manager, error) {
	if totalLimit < 1 || heavyLimit < 1 || heavyLimit > totalLimit {
		return nil, errors.New("invalid task concurrency limits")
	}
	if runner == nil {
		return nil, errors.New("task runner is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create task directory: %w", err)
	}
	m := &Manager{
		dir:      dir,
		runner:   runner,
		totalSem: make(chan struct{}, totalLimit),
		heavySem: make(chan struct{}, heavyLimit),
		tasks:    map[string]*Task{},
		cancel:   map[string]context.CancelFunc{},
		waiters:  map[string][]chan struct{}{},
		closed:   make(chan struct{}),
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) Submit(tool string, arguments any, heavy, durable bool) (Task, error) {
	if tool == "" {
		return Task{}, errors.New("tool is required")
	}
	raw, err := json.Marshal(arguments)
	if err != nil {
		return Task{}, fmt.Errorf("encode task arguments: %w", err)
	}
	id, err := newID()
	if err != nil {
		return Task{}, err
	}
	now := time.Now().UTC()
	task := &Task{
		ID:        id,
		Tool:      tool,
		Arguments: raw,
		Status:    StatusQueued,
		Message:   "等待执行",
		Heavy:     heavy,
		Durable:   durable,
		CreatedAt: now,
		UpdatedAt: now,
	}
	m.mu.Lock()
	select {
	case <-m.closed:
		m.mu.Unlock()
		return Task{}, errors.New("task manager is closed")
	default:
	}
	m.tasks[id] = task
	if err := m.persistLocked(task); err != nil {
		delete(m.tasks, id)
		m.mu.Unlock()
		return Task{}, err
	}
	copy := cloneTask(task)
	m.mu.Unlock()
	go m.run(id)
	return copy, nil
}

// ResumeInterrupted restarts durable tasks that were interrupted by a process
// restart. Non-durable interrupted tasks remain queryable and failed.
func (m *Manager) ResumeInterrupted() int {
	m.mu.Lock()
	var ids []string
	for _, task := range m.tasks {
		if task.Status != StatusInterrupted || !task.Durable {
			continue
		}
		task.Status = StatusQueued
		task.Message = "服务重启后恢复执行"
		task.UpdatedAt = time.Now().UTC()
		_ = m.persistLocked(task)
		ids = append(ids, task.ID)
	}
	m.mu.Unlock()
	for _, id := range ids {
		go m.run(id)
	}
	return len(ids)
}

func (m *Manager) Get(id string) (Task, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	task, ok := m.tasks[id]
	if !ok {
		return Task{}, false
	}
	return cloneTask(task), true
}

func (m *Manager) List() []Task {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]Task, 0, len(m.tasks))
	for _, task := range m.tasks {
		result = append(result, cloneTask(task))
	}
	slices.SortFunc(result, func(a, b Task) int {
		if a.CreatedAt.After(b.CreatedAt) {
			return -1
		}
		if a.CreatedAt.Before(b.CreatedAt) {
			return 1
		}
		return 0
	})
	return result
}

func (m *Manager) Cancel(id string) (Task, error) {
	m.mu.Lock()
	task, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return Task{}, os.ErrNotExist
	}
	if task.Terminal() {
		copy := cloneTask(task)
		m.mu.Unlock()
		return copy, nil
	}
	task.Status = StatusCancelling
	task.Message = "正在取消"
	task.UpdatedAt = time.Now().UTC()
	cancel := m.cancel[id]
	if err := m.persistLocked(task); err != nil {
		m.mu.Unlock()
		return Task{}, err
	}
	copy := cloneTask(task)
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return copy, nil
}

// Update accepts progress notifications for the MCP task extension. It does
// not permit a client to forge a terminal result.
func (m *Manager) Update(id string, progress float64, message string) (Task, error) {
	if progress < 0 || progress > 1 {
		return Task{}, errors.New("progress must be between 0 and 1")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	task, ok := m.tasks[id]
	if !ok {
		return Task{}, os.ErrNotExist
	}
	if task.Terminal() {
		return cloneTask(task), errors.New("terminal task cannot be updated")
	}
	task.Progress = progress
	task.Message = message
	task.UpdatedAt = time.Now().UTC()
	if err := m.persistLocked(task); err != nil {
		return Task{}, err
	}
	return cloneTask(task), nil
}

func (m *Manager) Wait(ctx context.Context, id string) (Task, error) {
	if task, ok := m.Get(id); !ok {
		return Task{}, os.ErrNotExist
	} else if task.Terminal() {
		return task, nil
	}
	ch := make(chan struct{})
	m.mu.Lock()
	task, ok := m.tasks[id]
	if !ok {
		m.mu.Unlock()
		return Task{}, os.ErrNotExist
	}
	if task.Terminal() {
		copy := cloneTask(task)
		m.mu.Unlock()
		return copy, nil
	}
	m.waiters[id] = append(m.waiters[id], ch)
	m.mu.Unlock()
	select {
	case <-ctx.Done():
		return Task{}, ctx.Err()
	case <-ch:
		task, ok := m.Get(id)
		if !ok {
			return Task{}, os.ErrNotExist
		}
		return task, nil
	}
}

func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		close(m.closed)
		m.mu.Lock()
		for _, cancel := range m.cancel {
			cancel()
		}
		m.mu.Unlock()
	})
}

func (m *Manager) run(id string) {
	select {
	case m.totalSem <- struct{}{}:
		defer func() { <-m.totalSem }()
	case <-m.closed:
		m.finishCancelled(id, "服务正在停止")
		return
	}
	m.mu.RLock()
	task, ok := m.tasks[id]
	heavy := ok && task.Heavy
	m.mu.RUnlock()
	if !ok {
		return
	}
	if heavy {
		select {
		case m.heavySem <- struct{}{}:
			defer func() { <-m.heavySem }()
		case <-m.closed:
			m.finishCancelled(id, "服务正在停止")
			return
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	task = m.tasks[id]
	if task == nil {
		m.mu.Unlock()
		cancel()
		return
	}
	if task.Status == StatusCancelling {
		m.mu.Unlock()
		cancel()
		m.finishCancelled(id, "任务在启动前取消")
		return
	}
	now := time.Now().UTC()
	task.Status = StatusRunning
	task.Message = "正在执行"
	task.StartedAt = now
	task.UpdatedAt = now
	m.cancel[id] = cancel
	_ = m.persistLocked(task)
	snapshot := cloneTask(task)
	m.mu.Unlock()

	reporter := &taskReporter{manager: m, id: id}
	result := m.runner(ctx, snapshot, reporter)
	wasCancelled := errors.Is(ctx.Err(), context.Canceled)
	cancel()

	m.mu.Lock()
	delete(m.cancel, id)
	task = m.tasks[id]
	if task == nil {
		m.mu.Unlock()
		return
	}
	now = time.Now().UTC()
	task.UpdatedAt = now
	task.CompletedAt = now
	task.Result = &result
	switch {
	case task.Status == StatusCancelling || wasCancelled:
		task.Status = StatusCancelled
		task.Message = "已取消"
	case result.Success:
		task.Status = StatusSucceeded
		task.Progress = 1
		task.Message = result.Message
	case result.Code == "TIMEOUT":
		task.Status = StatusTimedOut
		task.Message = result.Message
	default:
		task.Status = StatusFailed
		task.Message = result.Message
	}
	_ = m.persistLocked(task)
	m.notifyLocked(id)
	m.mu.Unlock()
}

func (m *Manager) finishCancelled(id, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task := m.tasks[id]
	if task == nil || task.Terminal() {
		return
	}
	now := time.Now().UTC()
	task.Status = StatusCancelled
	task.Message = message
	task.UpdatedAt = now
	task.CompletedAt = now
	result := model.Failure("CANCELLED", message, "Cancelled", message)
	task.Result = &result
	_ = m.persistLocked(task)
	m.notifyLocked(id)
}

func (m *Manager) load() error {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return fmt.Errorf("read task directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(m.dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read task snapshot %s: %w", path, err)
		}
		var task Task
		if err := json.Unmarshal(data, &task); err != nil {
			bad := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405Z")
			if renameErr := os.Rename(path, bad); renameErr != nil {
				return fmt.Errorf("quarantine task snapshot %s: %w", path, renameErr)
			}
			continue
		}
		if task.ID == "" || task.Tool == "" {
			continue
		}
		if task.Status == StatusRunning || task.Status == StatusCancelling || task.Status == StatusQueued {
			now := time.Now().UTC()
			task.Status = StatusInterrupted
			task.Message = "上次进程退出时任务尚未完成"
			task.UpdatedAt = now
			if !task.Durable {
				task.Status = StatusFailed
				task.CompletedAt = now
				failed := model.Failure("PROCESS_RESTARTED", task.Message, "Interrupted", task.Message)
				task.Result = &failed
			}
			if err := m.persistLocked(&task); err != nil {
				return err
			}
		}
		copy := task
		m.tasks[task.ID] = &copy
	}
	return nil
}

func (m *Manager) persistLocked(task *Task) error {
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return fmt.Errorf("encode task %s: %w", task.ID, err)
	}
	data = append(data, '\n')
	return atomicfile.Write(filepath.Join(m.dir, task.ID+".json"), data, 0o600)
}

func (m *Manager) notifyLocked(id string) {
	for _, waiter := range m.waiters[id] {
		close(waiter)
	}
	delete(m.waiters, id)
}

type taskReporter struct {
	manager *Manager
	id      string
}

func (r *taskReporter) Progress(value float64, message string) error {
	_, err := r.manager.Update(r.id, value, message)
	return err
}

func (r *taskReporter) Log(stream, text string) error {
	if stream != "stdout" && stream != "stderr" && stream != "event" {
		return errors.New("invalid task log stream")
	}
	line, err := json.Marshal(map[string]any{
		"time":   time.Now().UTC(),
		"stream": stream,
		"text":   text,
	})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(r.manager.dir, r.id+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func cloneTask(task *Task) Task {
	copy := *task
	if task.Arguments != nil {
		copy.Arguments = append(json.RawMessage(nil), task.Arguments...)
	}
	if task.Result != nil {
		resultCopy := *task.Result
		copy.Result = &resultCopy
	}
	return copy
}

func newID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate task id: %w", err)
	}
	// UUIDv4 shape, generated locally so no database or dependency is needed.
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	raw := hex.EncodeToString(bytes[:])
	return raw[0:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:32], nil
}
