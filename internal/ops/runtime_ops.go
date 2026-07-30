package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

func (m *Manager) publicShellOperation(ctx context.Context, req Request) Result {
	action, err := actionOf(req, "execute")
	if err != nil {
		return invalid(err.Error())
	}
	switch action {
	case "exec", "execute", "run":
		req.Tool = "shell_execute"
	case "execute_many", "run_many":
		req.Tool = "shell_execute_many"
	default:
		return invalidAction("zcr521_shell", action, "exec", "execute", "execute_many", "run", "run_many")
	}
	return m.shellOperation(ctx, req)
}

func (m *Manager) publicScriptOperation(ctx context.Context, req Request) Result {
	action, err := actionOf(req, "execute")
	if err != nil {
		return invalid(err.Error())
	}
	if action == "validate" {
		script, scriptErr := argOptionalString(req.Args, "", "script", "content")
		if scriptErr != nil {
			return invalid(scriptErr.Error())
		}
		if script == "" {
			pathValue, pathErr := argString(req.Args, "path")
			if pathErr != nil {
				return invalid("validate 需要 script/content 或 path")
			}
			path, pathErr := m.resolvePath(pathValue)
			if pathErr != nil {
				return invalid(pathErr.Error())
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return fileFailure("脚本读取失败", readErr, "shell_parse")
			}
			script = string(raw)
		}
		program, _ := shellProgram()
		if runtime.GOOS == "windows" {
			return unsupported("Windows 测试主机不提供 POSIX sh -n；Android 上会执行真实语法检查")
		}
		return m.runCommand(ctx, commandSpec{Name: program, Args: []string{"-n", "-c", script}, Dir: m.cfg.WorkDir, Timeout: m.cfg.ShellTimeout, Strategy: "shell_parse"})
	}
	if action != "execute" && action != "run" {
		return invalidAction("zcr521_script", action, "execute", "run", "validate")
	}
	req.Tool = "shell_script"
	return m.shellOperation(ctx, req)
}

func (m *Manager) publicTaskOperation(req Request) Result {
	action, err := actionOf(req, "get")
	if err != nil {
		return invalid(err.Error())
	}
	switch action {
	case "get", "status":
		req.Tool = "task_get"
	case "list":
		req.Tool = "task_list"
	case "cancel":
		req.Tool = "task_cancel"
	case "clear":
		req.Tool = "task_clear"
	case "output":
		req.Tool = "task_output"
	case "logs":
		req.Tool = "task_output"
	case "update":
		return unsupported("任务进度更新由 durable task service 层负责，ops 不伪造更新")
	case "artifacts":
		req.Tool = "task_artifacts"
	default:
		return invalidAction("zcr521_task", action, "artifacts", "cancel", "clear", "get", "list", "logs", "output", "status", "update")
	}
	return m.taskOperation(req)
}

type processInfo struct {
	PID          int      `json:"pid"`
	PPID         int      `json:"ppid,omitempty"`
	UID          int      `json:"uid,omitempty"`
	Name         string   `json:"name"`
	Command      string   `json:"command,omitempty"`
	State        string   `json:"state,omitempty"`
	VmRSSBytes   int64    `json:"vmRssBytes,omitempty"`
	CPUTimeTicks uint64   `json:"cpuTimeTicks,omitempty"`
	OpenFiles    []string `json:"openFiles,omitempty"`
}

func (m *Manager) processOperation(ctx context.Context, req Request) Result {
	action, err := actionOf(req, "list")
	if err != nil {
		return invalid(err.Error())
	}
	if action == "fds" {
		action = "open_files"
	}
	switch action {
	case "list", "search":
		if _, err := os.Stat("/proc"); err != nil {
			return unsupported("当前主机没有可用的 Linux /proc")
		}
		query, _ := argOptionalString(req.Args, "", "query", "name", "package")
		processes, scanErr := scanProcesses(query)
		if scanErr != nil {
			return fail("IO_ERROR", "进程列表读取失败", scanErr, "procfs")
		}
		return ok("进程列表读取成功", map[string]any{"count": len(processes), "processes": processes}, "procfs")
	case "info", "open_files":
		pid64, parseErr := argInt64(req.Args, -1, "pid")
		if parseErr != nil || pid64 <= 0 {
			return invalid("pid 必须是正整数")
		}
		info, infoErr := readProcess(int(pid64), action == "open_files")
		if infoErr != nil {
			return fileFailure("进程信息读取失败", infoErr, "procfs")
		}
		return ok("进程信息读取成功", info, "procfs")
	case "signal", "kill", "force_kill":
		pid64, parseErr := argInt64(req.Args, -1, "pid")
		if parseErr != nil || pid64 <= 0 {
			return invalid("pid 必须是正整数")
		}
		signal := int64(15)
		if action == "force_kill" {
			signal = 9
		}
		if action == "signal" {
			signal, parseErr = argInt64(req.Args, 15, "signal")
			if parseErr != nil || signal <= 0 || signal > 64 {
				return invalid("signal 必须在 1 到 64 之间")
			}
		}
		if err := sendSignal(int(pid64), int(signal)); err != nil {
			return fail("COMMAND_FAILED", "进程信号发送失败", err, "syscall_kill")
		}
		return ok("进程信号已发送", map[string]any{"pid": pid64, "signal": signal}, "syscall_kill")
	case "renice", "priority":
		pid64, parseErr := argInt64(req.Args, -1, "pid")
		priority, priorityErr := argInt64(req.Args, 0, "priority")
		if parseErr != nil || pid64 <= 0 || priorityErr != nil || priority < -20 || priority > 19 {
			return invalid("pid 必须为正整数，priority 必须在 -20 到 19 之间")
		}
		if err := changePriority(int(pid64), int(priority)); err != nil {
			return fail("COMMAND_FAILED", "进程优先级修改失败", err, "setpriority")
		}
		return ok("进程优先级修改成功", map[string]any{"pid": pid64, "priority": priority}, "setpriority")
	default:
		return invalidAction(req.Tool, action, "force_kill", "info", "kill", "list", "open_files", "priority", "renice", "search", "signal")
	}
}

func scanProcesses(query string) ([]processInfo, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(query)
	processes := make([]processInfo, 0)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		info, err := readProcess(pid, false)
		if err != nil {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(info.Name+" "+info.Command), query) {
			continue
		}
		processes = append(processes, info)
	}
	sort.Slice(processes, func(i, j int) bool { return processes[i].PID < processes[j].PID })
	return processes, nil
}

func readProcess(pid int, includeFiles bool) (processInfo, error) {
	root := filepath.Join("/proc", strconv.Itoa(pid))
	statusRaw, err := os.ReadFile(filepath.Join(root, "status"))
	if err != nil {
		return processInfo{}, err
	}
	info := processInfo{PID: pid, UID: -1}
	for _, line := range strings.Split(string(statusRaw), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch strings.TrimSuffix(fields[0], ":") {
		case "Name":
			info.Name = fields[1]
		case "State":
			info.State = strings.Join(fields[1:], " ")
		case "PPid":
			info.PPID, _ = strconv.Atoi(fields[1])
		case "Uid":
			info.UID, _ = strconv.Atoi(fields[1])
		case "VmRSS":
			value, _ := strconv.ParseInt(fields[1], 10, 64)
			info.VmRSSBytes = value * 1024
		}
	}
	if raw, readErr := os.ReadFile(filepath.Join(root, "cmdline")); readErr == nil {
		info.Command = strings.TrimSpace(strings.ReplaceAll(string(raw), "\x00", " "))
	}
	if raw, readErr := os.ReadFile(filepath.Join(root, "stat")); readErr == nil {
		// The command name can contain spaces and parentheses; fields after the
		// final ')' begin with state, ppid ... utime is field 14.
		if index := strings.LastIndex(string(raw), ")"); index >= 0 {
			fields := strings.Fields(string(raw)[index+1:])
			if len(fields) > 12 {
				utime, _ := strconv.ParseUint(fields[11], 10, 64)
				stime, _ := strconv.ParseUint(fields[12], 10, 64)
				info.CPUTimeTicks = utime + stime
			}
		}
	}
	if includeFiles {
		fdRoot := filepath.Join(root, "fd")
		entries, readErr := os.ReadDir(fdRoot)
		if readErr != nil {
			return info, readErr
		}
		for _, entry := range entries {
			if target, readErr := os.Readlink(filepath.Join(fdRoot, entry.Name())); readErr == nil {
				info.OpenFiles = append(info.OpenFiles, target)
			}
			if len(info.OpenFiles) >= 10000 {
				break
			}
		}
	}
	return info, nil
}

func parsePID(value string) (int, error) {
	pid, err := strconv.Atoi(value)
	if err != nil || pid <= 0 {
		return 0, errors.New("PID 必须是正整数")
	}
	return pid, nil
}

func processNotFound(pid int) Result {
	return fail("NOT_FOUND", "进程不存在", fmt.Errorf("pid %d", pid), "procfs")
}
