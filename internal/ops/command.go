package ops

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

const inlineOutputLimit = 1024 * 1024

type commandSpec struct {
	Name          string
	Args          []string
	Dir           string
	Env           map[string]string
	Stdin         string
	Timeout       time.Duration
	UID           int
	GID           int
	UseCredential bool
	Strategy      string
}

type boundedLog struct {
	mu        sync.Mutex
	file      *os.File
	inline    bytes.Buffer
	limit     int
	truncated bool
}

func newBoundedLog(path string, limit int) (*boundedLog, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	file, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	_ = file.Chmod(0o600)
	return &boundedLog{file: file, limit: limit}, nil
}

func (w *boundedLog) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	written, err := w.file.Write(data)
	if w.inline.Len() < w.limit {
		remaining := w.limit - w.inline.Len()
		if remaining > len(data) {
			remaining = len(data)
		}
		_, _ = w.inline.Write(data[:remaining])
	}
	if w.inline.Len() >= w.limit && len(data) > 0 {
		w.truncated = true
	}
	return written, err
}

func (w *boundedLog) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Close()
}

func (w *boundedLog) Text() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	text := w.inline.String()
	if w.truncated {
		text += "\n[输出已截断，完整内容见 artifacts]"
	}
	return text
}

func (m *Manager) runCommand(ctx context.Context, spec commandSpec) Result {
	if strings.TrimSpace(spec.Name) == "" {
		return invalid("命令名不能为空")
	}
	resolved, err := exec.LookPath(spec.Name)
	if err != nil {
		return unavailable(spec.Name)
	}
	if spec.Timeout <= 0 {
		spec.Timeout = m.cfg.ShellTimeout
	}
	if spec.Dir == "" {
		spec.Dir = m.cfg.WorkDir
	}
	if err := m.ensureRuntimeDirs(); err != nil {
		return fail("IO_ERROR", "无法初始化命令日志目录", err, "command")
	}
	if stat, statErr := os.Stat(spec.Dir); statErr != nil || !stat.IsDir() {
		if statErr == nil {
			statErr = errors.New("不是目录")
		}
		return fail("NOT_FOUND", "命令工作目录不可用", statErr, "command")
	}

	id := randomTaskID()
	logDir := filepath.Join(m.cfg.StateDir, "task-logs")
	stdoutPath := filepath.Join(logDir, id+".stdout")
	stderrPath := filepath.Join(logDir, id+".stderr")
	stdout, err := newBoundedLog(stdoutPath, inlineOutputLimit)
	if err != nil {
		return fail("IO_ERROR", "无法创建标准输出日志", err, "command")
	}
	defer stdout.Close()
	stderr, err := newBoundedLog(stderrPath, inlineOutputLimit)
	if err != nil {
		return fail("IO_ERROR", "无法创建标准错误日志", err, "command")
	}
	defer stderr.Close()

	cmd := exec.Command(resolved, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if spec.Stdin != "" {
		cmd.Stdin = strings.NewReader(spec.Stdin)
	}
	cmd.Env = mergeEnvironment(spec.Env)
	if err := configureProcess(cmd, spec); err != nil {
		return fail("INVALID_ARGUMENT", "无法配置执行身份", err, "process_identity")
	}
	started := time.Now()
	if err := cmd.Start(); err != nil {
		return fail(classifyCommandError(err, stderr.Text()), "命令无法启动", err, spec.Strategy)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(spec.Timeout)
	defer timer.Stop()
	var waitErr error
	timedOut := false
	cancelled := false
	select {
	case waitErr = <-done:
	case <-ctx.Done():
		cancelled = true
		terminateProcessTree(cmd)
		select {
		case waitErr = <-done:
		case <-time.After(2 * time.Second):
			killProcessTree(cmd)
			waitErr = <-done
		}
	case <-timer.C:
		timedOut = true
		terminateProcessTree(cmd)
		select {
		case waitErr = <-done:
		case <-time.After(2 * time.Second):
			killProcessTree(cmd)
			waitErr = <-done
		}
	}

	_ = stdout.Close()
	_ = stderr.Close()
	exitCode := processExitCode(cmd, waitErr)
	artifacts := []string{}
	if stdout.truncated {
		artifacts = append(artifacts, stdoutPath)
	}
	if stderr.truncated {
		artifacts = append(artifacts, stderrPath)
	}
	result := Result{
		Success:    waitErr == nil && !timedOut && !cancelled,
		Code:       "OK",
		Message:    "命令执行成功",
		Stdout:     stdout.Text(),
		Stderr:     stderr.Text(),
		ExitCode:   exitCode,
		DurationMs: time.Since(started).Milliseconds(),
		Artifacts:  artifacts,
		Strategy:   spec.Strategy,
	}
	switch {
	case timedOut:
		result.Success = false
		result.Code = "TIMEOUT"
		result.Message = "命令执行超时，进程组已终止"
		result.Error = fmt.Sprintf("timeout after %s", spec.Timeout)
	case cancelled:
		result.Success = false
		result.Code = "CANCELLED"
		result.Message = "命令已取消，进程组已终止"
		result.Error = ctx.Err().Error()
	case waitErr != nil:
		result.Success = false
		result.Code = classifyCommandError(waitErr, result.Stderr)
		result.Message = "命令执行失败"
		result.Error = waitErr.Error()
	}
	return result
}

func mergeEnvironment(overrides map[string]string) []string {
	values := make(map[string]string)
	for _, item := range os.Environ() {
		if index := strings.IndexByte(item, '='); index > 0 {
			values[item[:index]] = item[index+1:]
		}
	}
	for key, value := range overrides {
		if strings.Contains(key, "=") || strings.ContainsRune(key, '\x00') {
			continue
		}
		values[key] = value
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+values[key])
	}
	return out
}

func processExitCode(cmd *exec.Cmd, waitErr error) int {
	if cmd != nil && cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode()
	}
	if waitErr == nil {
		return 0
	}
	return -1
}

func classifyCommandError(err error, stderr string) string {
	lower := strings.ToLower(stderr + " " + err.Error())
	switch {
	case strings.Contains(lower, "avc: denied") || strings.Contains(lower, "permission denied") && strings.Contains(lower, "selinux"):
		return "SELINUX_DENIED"
	case strings.Contains(lower, "permission denied") || strings.Contains(lower, "operation not permitted"):
		return "PERMISSION_DENIED"
	case strings.Contains(lower, "not found") || strings.Contains(lower, "no such file"):
		return "COMMAND_UNAVAILABLE"
	default:
		return "COMMAND_FAILED"
	}
}

func shellProgram() (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd.exe", []string{"/D", "/S", "/C"}
	}
	if _, err := os.Stat("/system/bin/sh"); err == nil {
		return "/system/bin/sh", []string{"-c"}
	}
	return "/bin/sh", []string{"-c"}
}

func (m *Manager) shellOperation(ctx context.Context, req Request) Result {
	var command string
	var err error
	switch req.Tool {
	case "shell_execute", "shell_exec", "execute_command", "command_run":
		command, err = argString(req.Args, "command", "cmd")
	case "shell_execute_many", "execute_commands":
		var commands []string
		commands, err = argStringSlice(req.Args, "commands")
		command = strings.Join(commands, "\n")
	case "shell_script", "script_execute", "script_run":
		if script, ok := req.Args["script"].(string); ok && script != "" {
			command = script
		} else {
			var path string
			path, err = argString(req.Args, "path")
			if err == nil {
				path, err = m.resolvePath(path)
			}
			if err == nil {
				command = shellQuote(path)
			}
		}
	default:
		return unsupported("未知 Shell 操作")
	}
	if err != nil {
		return invalid(err.Error())
	}
	cwd, err := argOptionalString(req.Args, m.cfg.WorkDir, "cwd", "workDir")
	if err != nil {
		return invalid(err.Error())
	}
	cwd, err = m.resolvePath(cwd)
	if err != nil {
		return invalid(err.Error())
	}
	env, err := argStringMap(req.Args, "env")
	if err != nil {
		return invalid(err.Error())
	}
	stdin, err := argOptionalString(req.Args, "", "stdin")
	if err != nil {
		return invalid(err.Error())
	}
	timeout, err := argDuration(req.Args, m.cfg.ShellTimeout)
	if err != nil {
		return invalid(err.Error())
	}
	program, prefix := shellProgram()
	spec := commandSpec{
		Name:     program,
		Args:     append(prefix, command),
		Dir:      cwd,
		Env:      env,
		Stdin:    stdin,
		Timeout:  timeout,
		Strategy: "shell_process_group",
	}
	identity, err := argOptionalString(req.Args, "root", "identity", "user")
	if err != nil {
		return invalid(err.Error())
	}
	switch strings.ToLower(identity) {
	case "", "current":
	case "root":
		if platformEUID() != 0 {
			return fail("ROOT_UNAVAILABLE", "当前服务进程不是 uid=0，拒绝伪装 Root 执行成功", errors.New("effective uid is not 0"), "identity_check")
		}
	case "shell":
		spec.UID, spec.GID, spec.UseCredential = 2000, 2000, true
	default:
		uid, parseErr := parseIdentity(identity)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		gid64, gidErr := argInt64(req.Args, int64(uid), "gid")
		if gidErr != nil || gid64 < 0 {
			return invalid("gid 必须是非负整数")
		}
		spec.UID, spec.GID, spec.UseCredential = uid, int(gid64), true
	}
	return m.runCommand(ctx, spec)
}

func parseIdentity(value string) (int, error) {
	var uid int
	if _, err := fmt.Sscanf(value, "%d", &uid); err != nil || uid < 0 {
		return 0, fmt.Errorf("identity 必须是 root、shell、current 或数字 UID")
	}
	return uid, nil
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func copyStream(dst io.Writer, src io.Reader) error {
	_, err := io.CopyBuffer(dst, src, make([]byte, 128*1024))
	return err
}
