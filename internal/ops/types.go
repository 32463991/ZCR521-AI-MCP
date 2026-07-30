// Package ops implements the privileged operation layer used by the MCP
// transport.  It deliberately has no dependency on the HTTP or MCP packages so
// it can also be exercised by the module diagnostics and unit tests.
package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Request is the stable operation request passed by the MCP broker.
type Request struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args,omitempty"`
}

// Result is the stable, machine-readable operation result. ExitCode is -1 when
// no child process was started.
type Result struct {
	Success        bool     `json:"success"`
	Code           string   `json:"code"`
	Message        string   `json:"message"`
	Data           any      `json:"data,omitempty"`
	Error          string   `json:"error,omitempty"`
	Stdout         string   `json:"stdout,omitempty"`
	Stderr         string   `json:"stderr,omitempty"`
	ExitCode       int      `json:"exitCode"`
	DurationMs     int64    `json:"durationMs"`
	TaskID         string   `json:"taskId,omitempty"`
	RebootRequired bool     `json:"rebootRequired"`
	Artifacts      []string `json:"artifacts,omitempty"`
	Strategy       string   `json:"strategy,omitempty"`
}

// Config configures an operation Manager.
type Config struct {
	WorkDir      string
	StateDir     string
	ShellTimeout time.Duration
}

// Manager owns the operation configuration and background task registry.
type Manager struct {
	cfg                  Config
	tasksMu              sync.RWMutex
	tasks                map[string]*taskState
	taskLimit            int
	schedulesMu          sync.RWMutex
	schedules            map[string]*scheduleState
	scheduleOnce         sync.Once
	scheduleChanges      chan struct{}
	scheduleEvents       chan scheduleEvent
	scheduleEventMu      sync.Mutex
	scheduleEventCancels map[string]context.CancelFunc
	scheduleEventErrors  map[string]string
}

// New returns a self-contained operation manager. Existing user content is
// never removed. Directory creation errors are reported by Execute so startup
// of the MCP transport is not made fatal by unavailable shared storage.
func New(cfg Config) *Manager {
	if strings.TrimSpace(cfg.WorkDir) == "" {
		cfg.WorkDir = defaultWorkDir()
	}
	if strings.TrimSpace(cfg.StateDir) == "" {
		cfg.StateDir = filepath.Join(cfg.WorkDir, ".zcr521-state")
	}
	if cfg.ShellTimeout <= 0 {
		cfg.ShellTimeout = 60 * time.Second
	}
	return &Manager{
		cfg:                  cfg,
		tasks:                make(map[string]*taskState),
		taskLimit:            128,
		schedules:            make(map[string]*scheduleState),
		scheduleChanges:      make(chan struct{}, 1),
		scheduleEvents:       make(chan scheduleEvent, 16),
		scheduleEventCancels: make(map[string]context.CancelFunc),
		scheduleEventErrors:  make(map[string]string),
	}
}

func defaultWorkDir() string {
	if runtime.GOOS == "android" {
		return "/storage/emulated/0/zcr521AI"
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

func ok(message string, data any, strategy string) Result {
	return Result{
		Success:  true,
		Code:     "OK",
		Message:  message,
		Data:     data,
		ExitCode: 0,
		Strategy: strategy,
	}
}

func fail(code, message string, err error, strategy string) Result {
	r := Result{
		Success:  false,
		Code:     code,
		Message:  message,
		ExitCode: -1,
		Strategy: strategy,
	}
	if err != nil {
		r.Error = err.Error()
	}
	return r
}

func invalid(message string) Result {
	return fail("INVALID_ARGUMENT", message, errors.New(message), "validation")
}

func unsupported(message string) Result {
	return fail("UNSUPPORTED", message, errors.New(message), "capability_probe")
}

func unavailable(command string) Result {
	message := fmt.Sprintf("当前系统缺少可用命令：%s", command)
	return fail("COMMAND_UNAVAILABLE", message, errors.New(message), "command_probe")
}

func normalizeTool(tool string) string {
	tool = strings.TrimSpace(strings.ToLower(tool))
	replacer := strings.NewReplacer(".", "_", "-", "_", "/", "_", ":", "_", " ", "_")
	tool = replacer.Replace(tool)
	for strings.Contains(tool, "__") {
		tool = strings.ReplaceAll(tool, "__", "_")
	}
	return strings.Trim(tool, "_")
}

func (m *Manager) resolvePath(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("路径不能为空")
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	if strings.TrimSpace(m.cfg.WorkDir) == "" {
		return "", errors.New("未配置工作目录")
	}
	return filepath.Clean(filepath.Join(m.cfg.WorkDir, value)), nil
}

func (m *Manager) ensureRuntimeDirs() error {
	for _, dir := range []string{m.cfg.WorkDir, m.cfg.StateDir, filepath.Join(m.cfg.StateDir, "task-logs")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("创建目录 %s: %w", dir, err)
		}
	}
	return nil
}

func setDuration(start time.Time, r Result) Result {
	r.DurationMs = time.Since(start).Milliseconds()
	if r.Artifacts == nil {
		r.Artifacts = []string{}
	}
	return r
}

func jsonClone(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var out any
	if json.Unmarshal(raw, &out) != nil {
		return value
	}
	return out
}

// Execute validates, dispatches and measures one operation. Panics in a single
// tool are converted into INTERNAL_ERROR and never terminate the MCP service.
func (m *Manager) Execute(ctx context.Context, req Request) (result Result) {
	started := time.Now()
	defer func() {
		if recovered := recover(); recovered != nil {
			result = fail("INTERNAL_ERROR", "工具执行发生内部错误", fmt.Errorf("%v", recovered), "panic_guard")
		}
		result = setDuration(started, result)
	}()
	if ctx == nil {
		ctx = context.Background()
	}
	req.Tool = normalizeTool(req.Tool)
	if req.Tool == "" {
		return invalid("tool 不能为空")
	}
	if req.Args == nil {
		req.Args = map[string]any{}
	}
	req = normalizePublicRequest(req)
	if background, _ := argBool(req.Args, "background", false); background && !isTaskControl(req.Tool) {
		return m.startBackground(req)
	}
	return m.executeSync(ctx, req)
}
