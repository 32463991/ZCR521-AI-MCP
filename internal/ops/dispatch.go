package ops

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
)

var publicTools = []string{
	"zcr521_status",
	"zcr521_capabilities",
	"zcr521_config",
	"zcr521_fs_info",
	"zcr521_fs_read",
	"zcr521_fs_write",
	"zcr521_fs_manage",
	"zcr521_fs_search",
	"zcr521_fs_hash",
	"zcr521_archive",
	"zcr521_download",
	"zcr521_transfer_upload",
	"zcr521_transfer_export",
	"zcr521_shell",
	"zcr521_script",
	"zcr521_task",
	"zcr521_app_list",
	"zcr521_app_info",
	"zcr521_app_install",
	"zcr521_app_manage",
	"zcr521_app_permission",
	"zcr521_app_export",
	"zcr521_root_info",
	"zcr521_root_module",
	"zcr521_systemless",
	"zcr521_process",
	"zcr521_service",
	"zcr521_property",
	"zcr521_setting",
	"zcr521_display",
	"zcr521_audio",
	"zcr521_connectivity",
	"zcr521_locale_time",
	"zcr521_input_method",
	"zcr521_app_policy",
	"zcr521_default_app",
	"zcr521_notification",
	"zcr521_accessibility",
	"zcr521_developer",
	"zcr521_device_info",
	"zcr521_power",
	"zcr521_screen",
	"zcr521_input",
	"zcr521_network",
	"zcr521_log",
	"zcr521_diagnostics",
	"zcr521_schedule",
	"zcr521_backup",
}

// SupportedTools returns a copy of the stable public tool-name list.
func SupportedTools() []string {
	return append([]string(nil), publicTools...)
}

func (m *Manager) executeSync(ctx context.Context, req Request) Result {
	switch req.Tool {
	case "zcr521_status":
		return m.statusOperation(req)
	case "zcr521_capabilities":
		return m.capabilityOperation(req)
	case "zcr521_config":
		return m.configOperation(req)
	case "zcr521_fs_info":
		return m.fsInfoOperation(req)
	case "zcr521_fs_read":
		return m.fsReadOperation(req)
	case "zcr521_fs_write":
		return m.fsWriteOperation(req)
	case "zcr521_fs_manage":
		return m.fsManageOperation(req)
	case "zcr521_fs_search":
		return m.fsSearchOperation(req)
	case "zcr521_fs_hash":
		return m.fsHashOperation(req)
	case "zcr521_archive":
		return m.archiveOperation(ctx, req)
	case "zcr521_download":
		return m.downloadOperation(ctx, req)
	case "zcr521_transfer_upload":
		return m.uploadOperation(req)
	case "zcr521_transfer_export":
		return m.exportOperation(req)
	case "zcr521_shell":
		return m.publicShellOperation(ctx, req)
	case "zcr521_script":
		return m.publicScriptOperation(ctx, req)
	case "zcr521_task":
		return m.publicTaskOperation(req)
	case "zcr521_process":
		return m.processOperation(ctx, req)
	case "zcr521_network":
		return m.networkOperation(ctx, req)
	case "zcr521_app_list", "zcr521_app_info", "zcr521_app_install",
		"zcr521_app_manage", "zcr521_app_permission", "zcr521_app_export":
		return m.appOperation(ctx, req)
	case "zcr521_root_info", "zcr521_root_module", "zcr521_systemless":
		return m.rootOperation(ctx, req)
	case "zcr521_service":
		return m.serviceOperation(ctx, req)
	case "zcr521_property", "zcr521_setting", "zcr521_display", "zcr521_audio",
		"zcr521_connectivity", "zcr521_locale_time", "zcr521_input_method",
		"zcr521_app_policy", "zcr521_default_app", "zcr521_notification",
		"zcr521_accessibility", "zcr521_developer":
		return m.systemOperation(ctx, req)
	case "zcr521_device_info":
		return m.deviceOperation(ctx, req)
	case "zcr521_power":
		return m.powerOperation(ctx, req)
	case "zcr521_screen":
		return m.screenOperation(ctx, req)
	case "zcr521_input":
		return m.inputOperation(ctx, req)
	case "zcr521_log":
		return m.logOperation(ctx, req)
	case "zcr521_diagnostics":
		return m.diagnosticsOperation(ctx, req)
	case "zcr521_schedule":
		return m.scheduleOperation(ctx, req)
	case "zcr521_backup":
		return m.backupOperation(ctx, req)
	// Internal aliases are useful to unit-test the primitive operation layer.
	case "shell_execute", "shell_exec", "execute_command", "command_run",
		"shell_execute_many", "execute_commands", "shell_script", "script_execute", "script_run":
		return m.shellOperation(ctx, req)
	case "task_get", "task_status", "task_list", "task_cancel", "task_clear", "task_output", "task_artifacts":
		return m.taskOperation(req)
	default:
		return fail("UNKNOWN_TOOL", "未知工具："+req.Tool, fmt.Errorf("unknown tool %q", req.Tool), "dispatcher")
	}
}

func actionOf(req Request, fallback string) (string, error) {
	action, err := argOptionalString(req.Args, fallback, "action")
	if err != nil {
		return "", err
	}
	return normalizeTool(action), nil
}

func invalidAction(tool, action string, allowed ...string) Result {
	sort.Strings(allowed)
	message := fmt.Sprintf("%s 不支持 action=%q；允许值：%s", tool, action, strings.Join(allowed, ", "))
	return fail("INVALID_ARGUMENT", message, fmt.Errorf("unknown action %q", action), "action_validation")
}

func (m *Manager) statusOperation(req Request) Result {
	action, err := actionOf(req, "get")
	if err != nil {
		return invalid(err.Error())
	}
	if action != "get" && action != "status" {
		return invalidAction(req.Tool, action, "get", "status")
	}
	m.tasksMu.RLock()
	active := 0
	for _, task := range m.tasks {
		if task.Status == "waiting" || task.Status == "running" {
			active++
		}
	}
	m.tasksMu.RUnlock()
	return ok("运行状态读取成功", map[string]any{
		"workDir":      m.cfg.WorkDir,
		"stateDir":     m.cfg.StateDir,
		"root":         platformEUID() == 0,
		"effectiveUid": platformEUID(),
		"goos":         runtime.GOOS,
		"goarch":       runtime.GOARCH,
		"activeTasks":  active,
	}, "native_runtime")
}

func (m *Manager) configOperation(req Request) Result {
	action, err := actionOf(req, "get")
	if err != nil {
		return invalid(err.Error())
	}
	if action != "get" {
		switch action {
		case "validate", "update", "export", "reset":
			return unsupported("配置持久化由 service/config 层负责，ops 不伪造配置写入结果")
		default:
			return invalidAction(req.Tool, action, "export", "get", "reset", "update", "validate")
		}
	}
	return ok("有效配置读取成功", map[string]any{
		"workDir":        m.cfg.WorkDir,
		"stateDir":       m.cfg.StateDir,
		"shellTimeoutMs": m.cfg.ShellTimeout.Milliseconds(),
	}, "in_memory_config")
}

func (m *Manager) capabilityOperation(req Request) Result {
	action, err := actionOf(req, "probe")
	if err != nil {
		return invalid(err.Error())
	}
	if action != "probe" && action != "get" {
		return invalidAction(req.Tool, action, "get", "probe")
	}
	commands := []string{
		"sh", "toybox", "busybox", "cmd", "pm", "am", "settings", "svc",
		"dumpsys", "input", "screencap", "screenrecord", "logcat", "dmesg",
		"tar", "gzip", "xz", "7z", "7za", "ping", "ip", "ss", "getprop",
		"setprop", "getenforce", "chcon", "restorecon", "magisk", "ksud", "apd",
	}
	available := make(map[string]string)
	missing := make([]string, 0)
	for _, command := range commands {
		if path, lookErr := exec.LookPath(command); lookErr == nil {
			available[command] = path
		} else {
			missing = append(missing, command)
		}
	}
	api := readFirstFile("/system/build.prop", "")
	if raw, readErr := os.ReadFile("/proc/version"); readErr == nil {
		api = strings.TrimSpace(string(raw))
	}
	return ok("能力探测完成", map[string]any{
		"tools":         SupportedTools(),
		"commands":      available,
		"missing":       missing,
		"root":          platformEUID() == 0,
		"effectiveUid":  platformEUID(),
		"runtime":       runtime.GOOS + "/" + runtime.GOARCH,
		"kernelVersion": api,
		"selinux":       detectSELinux(),
		"rootFramework": detectRootFramework(),
	}, "runtime_probe")
}

func readFirstFile(paths ...string) string {
	for _, path := range paths {
		if path == "" {
			continue
		}
		if raw, err := os.ReadFile(path); err == nil {
			return strings.TrimSpace(string(raw))
		}
	}
	return ""
}

func detectSELinux() map[string]any {
	enforce := readFirstFile("/sys/fs/selinux/enforce")
	state := "unavailable"
	switch enforce {
	case "1":
		state = "enforcing"
	case "0":
		state = "permissive"
	}
	return map[string]any{
		"state":      state,
		"filesystem": enforce != "",
	}
}

func detectRootFramework() map[string]any {
	type candidate struct {
		Name  string
		Paths []string
		Cmd   string
	}
	candidates := []candidate{
		{Name: "KernelSU", Paths: []string{"/data/adb/ksu", "/data/adb/ksu/bin/ksud"}, Cmd: "ksud"},
		{Name: "APatch", Paths: []string{"/data/adb/ap", "/data/adb/ap/bin/apd"}, Cmd: "apd"},
		{Name: "Magisk", Paths: []string{"/data/adb/magisk", "/sbin/.magisk"}, Cmd: "magisk"},
	}
	for _, item := range candidates {
		for _, path := range item.Paths {
			if _, err := os.Stat(path); err == nil {
				return map[string]any{"name": item.Name, "detectedBy": path}
			}
		}
		if path, err := exec.LookPath(item.Cmd); err == nil {
			return map[string]any{"name": item.Name, "detectedBy": path}
		}
	}
	return map[string]any{"name": "unknown", "detectedBy": ""}
}
