package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

func (m *Manager) deviceOperation(ctx context.Context, req Request) Result {
	if result := requireAndroid(); result != nil {
		return *result
	}
	action, err := actionOf(req, "get")
	if err != nil {
		return invalid(err.Error())
	}
	switch action {
	case "get", "info":
		properties := map[string]string{}
		for label, key := range map[string]string{
			"brand": "ro.product.brand", "manufacturer": "ro.product.manufacturer",
			"model": "ro.product.model", "device": "ro.product.device",
			"androidVersion": "ro.build.version.release", "apiLevel": "ro.build.version.sdk",
			"buildFingerprint": "ro.build.fingerprint", "abiList": "ro.product.cpu.abilist",
			"bootVerifiedState": "ro.boot.verifiedbootstate",
		} {
			properties[label] = strings.TrimSpace(getProp(ctx, m, key).Stdout)
		}
		memory := parseMemInfo(readFirstFile("/proc/meminfo"))
		storage, _ := platformDiskUsage(m.cfg.WorkDir)
		interfaces, _ := networkInterfaces()
		return ok("设备信息读取成功", map[string]any{
			"properties":    properties,
			"kernel":        readFirstFile("/proc/version"),
			"cpu":           readFirstFile("/proc/cpuinfo"),
			"memory":        memory,
			"storage":       storage,
			"battery":       commandData(m.runAndroid(ctx, commandVariant{Name: "dumpsys", Args: []string{"battery"}, Strategy: "dumpsys_battery"})),
			"network":       interfaces,
			"uptimeSeconds": readUptimeSeconds(),
			"selinux":       detectSELinux(),
			"rootFramework": detectRootFramework(),
			"bootCompleted": strings.TrimSpace(getProp(ctx, m, "sys.boot_completed").Stdout) == "1",
		}, "procfs_getprop_dumpsys")
	case "cpu":
		return ok("CPU 信息读取成功", map[string]any{"cpuinfo": readFirstFile("/proc/cpuinfo"), "abiList": strings.TrimSpace(getProp(ctx, m, "ro.product.cpu.abilist").Stdout)}, "procfs_getprop")
	case "memory":
		raw := readFirstFile("/proc/meminfo")
		if raw == "" {
			return fail("NOT_FOUND", "无法读取 /proc/meminfo", os.ErrNotExist, "procfs")
		}
		return ok("内存信息读取成功", parseMemInfo(raw), "procfs")
	case "storage":
		pathValue, parseErr := argOptionalString(req.Args, m.cfg.WorkDir, "path")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		path, parseErr := m.resolvePath(pathValue)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		usage, usageErr := platformDiskUsage(path)
		if usageErr != nil {
			return fileFailure("存储信息读取失败", usageErr, "statfs")
		}
		return ok("存储信息读取成功", usage, "statfs")
	case "battery":
		result := m.runAndroid(ctx, commandVariant{Name: "dumpsys", Args: []string{"battery"}, Strategy: "dumpsys_battery"})
		if result.Success {
			result.Data = parseColonLines(result.Stdout)
			result.Message = "电池信息读取成功"
		}
		return result
	case "sensors":
		return m.runAndroid(ctx, commandVariant{Name: "dumpsys", Args: []string{"sensorservice"}, Strategy: "dumpsys_sensorservice"})
	case "temperature":
		values := make(map[string]string)
		entries, _ := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
		for _, path := range entries {
			values[path] = readFirstFile(path)
		}
		if len(values) == 0 {
			return fail("NOT_FOUND", "没有可读的 thermal_zone 温度节点", os.ErrNotExist, "sysfs_thermal")
		}
		return ok("温度节点读取成功；单位由内核驱动定义，常见为毫摄氏度", values, "sysfs_thermal")
	case "network":
		interfaces, scanErr := networkInterfaces()
		if scanErr != nil {
			return fail("NETWORK_ERROR", "网络信息读取失败", scanErr, "net_interfaces")
		}
		return ok("网络信息读取成功", interfaces, "net_interfaces")
	case "uptime":
		return ok("运行时间读取成功", map[string]any{"seconds": readUptimeSeconds(), "raw": readFirstFile("/proc/uptime")}, "procfs")
	case "selinux":
		return ok("SELinux 状态读取成功", detectSELinux(), "selinuxfs")
	case "boot":
		return ok("启动状态读取成功", map[string]any{
			"sysBootCompleted":  strings.TrimSpace(getProp(ctx, m, "sys.boot_completed").Stdout),
			"devBootComplete":   strings.TrimSpace(getProp(ctx, m, "dev.bootcomplete").Stdout),
			"verifiedBootState": strings.TrimSpace(getProp(ctx, m, "ro.boot.verifiedbootstate").Stdout),
			"slotSuffix":        strings.TrimSpace(getProp(ctx, m, "ro.boot.slot_suffix").Stdout),
		}, "getprop_boot")
	default:
		return invalidAction(req.Tool, action, "battery", "boot", "cpu", "get", "info", "memory", "network", "selinux", "sensors", "storage", "temperature", "uptime")
	}
}

func commandData(result Result) map[string]any {
	return map[string]any{"success": result.Success, "code": result.Code, "stdout": result.Stdout, "stderr": result.Stderr}
}

func parseMemInfo(raw string) map[string]int64 {
	values := make(map[string]int64)
	for _, line := range strings.Split(raw, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		value, err := strconv.ParseInt(fields[1], 10, 64)
		if err == nil {
			if len(fields) > 2 && strings.EqualFold(fields[2], "kB") {
				value *= 1024
			}
			values[strings.TrimSuffix(fields[0], ":")] = value
		}
	}
	return values
}

func parseColonLines(raw string) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(raw, "\n") {
		if index := strings.IndexByte(line, ':'); index > 0 {
			values[strings.TrimSpace(line[:index])] = strings.TrimSpace(line[index+1:])
		}
	}
	return values
}

func readUptimeSeconds() float64 {
	fields := strings.Fields(readFirstFile("/proc/uptime"))
	if len(fields) == 0 {
		return 0
	}
	value, _ := strconv.ParseFloat(fields[0], 64)
	return value
}

func (m *Manager) powerOperation(ctx context.Context, req Request) Result {
	if result := requireAndroid(); result != nil {
		return *result
	}
	action, err := actionOf(req, "status")
	if err != nil {
		return invalid(err.Error())
	}
	switch action {
	case "status":
		return m.runAndroid(ctx, commandVariant{Name: "dumpsys", Args: []string{"power"}, Strategy: "dumpsys_power"})
	case "wake", "sleep":
		key := "KEYCODE_WAKEUP"
		if action == "sleep" {
			key = "KEYCODE_SLEEP"
		}
		return m.runAndroidRoot(ctx, commandVariant{Name: "input", Args: []string{"keyevent", key}, Strategy: "input_keyevent"})
	case "reboot", "shutdown", "recovery", "bootloader", "fastbootd", "soft_reboot":
		if result := requireAndroidRoot(); result != nil {
			return *result
		}
		command := "reboot"
		args := []string{}
		switch action {
		case "shutdown":
			command, args = "reboot", []string{"-p"}
		case "recovery":
			args = []string{"recovery"}
		case "bootloader":
			args = []string{"bootloader"}
		case "fastbootd":
			args = []string{"fastboot"}
		case "soft_reboot":
			command, args = "setprop", []string{"ctl.restart", "zygote"}
		}
		pid, scheduleErr := startDelayedAndroidCommand(command, args)
		if scheduleErr != nil {
			return fail("COMMAND_FAILED", "电源命令无法排队", scheduleErr, "delayed_power_command")
		}
		result := ok("电源命令已提交，将在约 2 秒后执行", map[string]any{"action": action, "helperPid": pid}, "delayed_power_command")
		result.RebootRequired = action != "shutdown"
		return result
	default:
		return invalidAction(req.Tool, action, "bootloader", "fastbootd", "reboot", "recovery", "shutdown", "sleep", "soft_reboot", "status", "wake")
	}
}

func startDelayedAndroidCommand(command string, args []string) (int, error) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		if _, statErr := os.Stat("/system/bin/sh"); statErr == nil {
			shell = "/system/bin/sh"
		} else {
			return 0, err
		}
	}
	commandPath, err := exec.LookPath(command)
	if err != nil {
		if _, statErr := os.Stat("/system/bin/" + command); statErr == nil {
			commandPath = "/system/bin/" + command
		} else {
			return 0, err
		}
	}
	parts := []string{"sleep 2; exec", shellQuote(commandPath)}
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	cmd := exec.Command(shell, "-c", strings.Join(parts, " "))
	cmd.Stdin = nil
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return pid, err
	}
	return pid, nil
}

func (m *Manager) screenOperation(ctx context.Context, req Request) Result {
	if result := requireAndroid(); result != nil {
		return *result
	}
	action, err := actionOf(req, "screenshot")
	if err != nil {
		return invalid(err.Error())
	}
	switch action {
	case "screenshot":
		defaultPath := filepath.Join(m.cfg.WorkDir, "output", "screenshots", "screenshot-"+time.Now().UTC().Format("20060102-150405")+".png")
		pathValue, parseErr := argOptionalString(req.Args, defaultPath, "path", "output")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		path, parseErr := m.resolvePath(pathValue)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o750); mkErr != nil {
			return fileFailure("截图目录创建失败", mkErr, "screencap")
		}
		result := m.runAndroidRoot(ctx, commandVariant{Name: "screencap", Args: []string{"-p", path}, Strategy: "screencap_cli"})
		if !result.Success {
			return result
		}
		info, statErr := os.Stat(path)
		if statErr != nil || info.Size() == 0 {
			return fail("VERIFY_FAILED", "截图命令结束但文件不存在或为空", statErr, "screencap_readback")
		}
		result.Message = "截图成功；受 FLAG_SECURE/DRM 保护的图层可能为空白"
		result.Data = map[string]any{"path": path, "bytes": info.Size(), "secureLayersMayBeBlank": true}
		result.Artifacts = []string{path}
		return result
	case "record":
		defaultPath := filepath.Join(m.cfg.WorkDir, "output", "records", "record-"+time.Now().UTC().Format("20060102-150405")+".mp4")
		pathValue, parseErr := argOptionalString(req.Args, defaultPath, "path", "output")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		path, parseErr := m.resolvePath(pathValue)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		duration, parseErr := argInt64(req.Args, 30, "duration", "seconds")
		if parseErr != nil || duration <= 0 || duration > 180 {
			return invalid("为兼容 API 26-36，录屏 duration 必须在 1 到 180 秒之间")
		}
		args := []string{"--time-limit", strconv.FormatInt(duration, 10)}
		if size, ok := req.Args["size"].(string); ok && size != "" {
			if !regexp.MustCompile(`^\d+x\d+$`).MatchString(size) {
				return invalid("size 必须形如 1080x1920")
			}
			args = append(args, "--size", size)
		}
		if bitrate, ok := req.Args["bitRate"]; ok {
			parsed, parseErr := argInt64(map[string]any{"bitRate": bitrate}, 0, "bitRate")
			if parseErr != nil || parsed <= 0 {
				return invalid("bitRate 必须是正整数")
			}
			args = append(args, "--bit-rate", strconv.FormatInt(parsed, 10))
		}
		args = append(args, path)
		if mkErr := os.MkdirAll(filepath.Dir(path), 0o750); mkErr != nil {
			return fileFailure("录屏目录创建失败", mkErr, "screenrecord")
		}
		result := m.runCommand(ctx, commandSpec{Name: "screenrecord", Args: args, Dir: m.cfg.WorkDir, Timeout: time.Duration(duration+15) * time.Second, Strategy: "screenrecord_cli"})
		if !result.Success {
			return result
		}
		info, statErr := os.Stat(path)
		if statErr != nil || info.Size() == 0 {
			return fail("VERIFY_FAILED", "录屏命令结束但文件不存在或为空", statErr, "screenrecord_readback")
		}
		result.Message = "录屏完成；screenrecord 不录制系统音频，受保护图层可能为空白"
		result.Data = map[string]any{"path": path, "bytes": info.Size(), "audio": false, "secureLayersMayBeBlank": true}
		result.Artifacts = append(result.Artifacts, path)
		return result
	case "wake", "sleep":
		key := "KEYCODE_WAKEUP"
		if action == "sleep" {
			key = "KEYCODE_SLEEP"
		}
		return m.runAndroidRoot(ctx, commandVariant{Name: "input", Args: []string{"keyevent", key}, Strategy: "input_screen_power"})
	case "size":
		return m.runAndroid(ctx, commandVariant{Name: "wm", Args: []string{"size"}, Strategy: "wm_size"})
	case "orientation":
		return m.runAndroid(ctx, commandVariant{Name: "dumpsys", Args: []string{"input"}, Strategy: "dumpsys_input"})
	case "foreground":
		return m.runAndroid(ctx,
			commandVariant{Name: "dumpsys", Args: []string{"activity", "activities"}, Strategy: "dumpsys_activity"},
			commandVariant{Name: "dumpsys", Args: []string{"window", "windows"}, Strategy: "dumpsys_window"},
		)
	default:
		return invalidAction(req.Tool, action, "foreground", "orientation", "record", "screenshot", "size", "sleep", "wake")
	}
}

func (m *Manager) inputOperation(ctx context.Context, req Request) Result {
	if result := requireAndroidRoot(); result != nil {
		return *result
	}
	action, err := actionOf(req, "")
	if err != nil || action == "" {
		return invalid("action 不能为空")
	}
	args := []string{}
	switch action {
	case "tap":
		x, xErr := argInt64(req.Args, -1, "x")
		y, yErr := argInt64(req.Args, -1, "y")
		if xErr != nil || yErr != nil || x < 0 || y < 0 {
			return invalid("tap 需要非负 x/y")
		}
		args = []string{"tap", strconv.FormatInt(x, 10), strconv.FormatInt(y, 10)}
	case "long_press":
		x, xErr := argInt64(req.Args, -1, "x")
		y, yErr := argInt64(req.Args, -1, "y")
		duration, durationErr := argInt64(req.Args, 800, "durationMs")
		if xErr != nil || yErr != nil || durationErr != nil || x < 0 || y < 0 || duration <= 0 {
			return invalid("long_press 需要非负 x/y 和正 durationMs")
		}
		args = []string{"swipe", strconv.FormatInt(x, 10), strconv.FormatInt(y, 10), strconv.FormatInt(x, 10), strconv.FormatInt(y, 10), strconv.FormatInt(duration, 10)}
	case "swipe":
		x1, e1 := argInt64(req.Args, -1, "x1")
		y1, e2 := argInt64(req.Args, -1, "y1")
		x2, e3 := argInt64(req.Args, -1, "x2")
		y2, e4 := argInt64(req.Args, -1, "y2")
		duration, e5 := argInt64(req.Args, 300, "durationMs")
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || x1 < 0 || y1 < 0 || x2 < 0 || y2 < 0 || duration <= 0 {
			return invalid("swipe 需要非负 x1/y1/x2/y2 和正 durationMs")
		}
		args = []string{"swipe", fmt.Sprint(x1), fmt.Sprint(y1), fmt.Sprint(x2), fmt.Sprint(y2), fmt.Sprint(duration)}
	case "text":
		text, parseErr := argString(req.Args, "text", "value")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		// Android input uses %s as a space escape. It cannot faithfully inject
		// every Unicode character; command failure is returned honestly.
		args = []string{"text", strings.ReplaceAll(text, " ", "%s")}
	case "keyevent", "key", "back", "home", "recents", "power", "volume_up", "volume_down":
		key := ""
		switch action {
		case "back":
			key = "KEYCODE_BACK"
		case "home":
			key = "KEYCODE_HOME"
		case "recents":
			key = "KEYCODE_APP_SWITCH"
		case "power":
			key = "KEYCODE_POWER"
		case "volume_up":
			key = "KEYCODE_VOLUME_UP"
		case "volume_down":
			key = "KEYCODE_VOLUME_DOWN"
		default:
			key, err = argString(req.Args, "key", "code")
			if err != nil {
				return invalid(err.Error())
			}
		}
		args = []string{"keyevent", key}
	case "statusbar":
		mode, modeErr := argOptionalString(req.Args, "notifications", "mode", "target")
		if modeErr != nil {
			return invalid(modeErr.Error())
		}
		subcommand := ""
		switch normalizeTool(mode) {
		case "notifications", "notification":
			subcommand = "expand-notifications"
		case "quick_settings", "settings":
			subcommand = "expand-settings"
		case "collapse", "close":
			subcommand = "collapse"
		default:
			return invalid("statusbar mode 必须是 notifications、quick_settings 或 collapse")
		}
		return m.runAndroidRoot(ctx, commandVariant{Name: "cmd", Args: []string{"statusbar", subcommand}, Strategy: "cmd_statusbar"})
	case "notifications", "quick_settings":
		subcommand := "expand-notifications"
		if action == "quick_settings" {
			subcommand = "expand-settings"
		}
		return m.runAndroidRoot(ctx, commandVariant{Name: "cmd", Args: []string{"statusbar", subcommand}, Strategy: "cmd_statusbar"})
	default:
		return invalidAction(req.Tool, action, "back", "home", "key", "keyevent", "long_press", "notifications", "power", "quick_settings", "recents", "statusbar", "swipe", "tap", "text", "volume_down", "volume_up")
	}
	return m.runAndroidRoot(ctx, commandVariant{Name: "input", Args: args, Strategy: "input_cli"})
}

func (m *Manager) logOperation(ctx context.Context, req Request) Result {
	if result := requireAndroid(); result != nil {
		return *result
	}
	action, err := actionOf(req, "logcat")
	if err != nil {
		return invalid(err.Error())
	}
	switch action {
	case "logcat", "stream":
		args := []string{"-v", "threadtime"}
		follow, _ := argBool(req.Args, "follow", action == "stream")
		if !follow {
			args = append(args, "-d")
		}
		lines, parseErr := argInt64(req.Args, 500, "lines")
		if parseErr != nil || lines <= 0 || lines > 100000 {
			return invalid("lines 必须在 1 到 100000 之间")
		}
		args = append(args, "-t", strconv.FormatInt(lines, 10))
		if tag, ok := req.Args["tag"].(string); ok && tag != "" {
			args = append(args, tag+":V", "*:S")
		}
		timeout, parseErr := argDuration(req.Args, 30*time.Second)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		return m.runCommand(ctx, commandSpec{Name: "logcat", Args: args, Dir: m.cfg.WorkDir, Timeout: timeout, Strategy: "logcat_cli"})
	case "kernel", "dmesg":
		return m.runAndroidRoot(ctx, commandVariant{Name: "dmesg", Args: []string{}, Strategy: "dmesg"})
	case "module":
		path := "/data/adb/modules/zcr521.android.mcp"
		logs := make(map[string]string)
		for _, name := range []string{"service.log", "install.log", "zcr521.log"} {
			if raw, readErr := os.ReadFile(filepath.Join(path, name)); readErr == nil {
				logs[name] = string(raw)
			}
		}
		if len(logs) == 0 {
			return fail("NOT_FOUND", "没有找到模块日志", os.ErrNotExist, "module_logs")
		}
		return ok("模块日志读取成功", logs, "module_logs")
	case "service":
		pathValue, parseErr := argOptionalString(req.Args, filepath.Join(m.cfg.StateDir, "service.log"), "path")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		path, parseErr := m.resolvePath(pathValue)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return fileFailure("MCP 服务日志读取失败", readErr, "file_log")
		}
		return ok("MCP 服务日志读取成功", map[string]any{"path": path, "content": string(raw)}, "file_log")
	case "clear":
		target, _ := argOptionalString(req.Args, "logcat", "target")
		if target != "logcat" {
			return unsupported("为避免误删诊断证据，clear 当前仅支持 target=logcat")
		}
		return m.runAndroidRoot(ctx, commandVariant{Name: "logcat", Args: []string{"-c"}, Strategy: "logcat_clear"})
	default:
		return invalidAction(req.Tool, action, "clear", "dmesg", "kernel", "logcat", "module", "service", "stream")
	}
}

func (m *Manager) diagnosticsOperation(ctx context.Context, req Request) Result {
	action, err := actionOf(req, "report")
	if err != nil {
		return invalid(err.Error())
	}
	if action != "report" && action != "check" {
		return invalidAction(req.Tool, action, "check", "report")
	}
	if result := requireAndroid(); result != nil {
		return *result
	}
	report := map[string]any{
		"generatedAt":  time.Now().UTC().Format(time.RFC3339Nano),
		"root":         commandData(m.rootInfo(ctx, Request{Tool: "zcr521_root_info", Args: map[string]any{"action": "get"}})),
		"selinux":      detectSELinux(),
		"framework":    detectRootFramework(),
		"workDir":      diagnosticPath(m.cfg.WorkDir),
		"stateDir":     diagnosticPath(m.cfg.StateDir),
		"port5322":     checkListeningPort("5322"),
		"architecture": strings.TrimSpace(getProp(ctx, m, "ro.product.cpu.abilist").Stdout),
		"apiLevel":     strings.TrimSpace(getProp(ctx, m, "ro.build.version.sdk").Stdout),
		"kernel":       readFirstFile("/proc/version"),
		"commands":     capabilityCommandMap(),
	}
	if action == "check" {
		return ok("自动诊断完成", report, "diagnostic_probe")
	}
	defaultPath := filepath.Join(m.cfg.WorkDir, "logs", "diagnostic-"+time.Now().UTC().Format("20060102-150405")+".json")
	pathValue, parseErr := argOptionalString(req.Args, defaultPath, "path", "output")
	if parseErr != nil {
		return invalid(parseErr.Error())
	}
	path, parseErr := m.resolvePath(pathValue)
	if parseErr != nil {
		return invalid(parseErr.Error())
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fileFailure("诊断目录创建失败", err, "diagnostic_report")
	}
	raw, marshalErr := json.MarshalIndent(report, "", "  ")
	if marshalErr != nil {
		return fail("INTERNAL_ERROR", "诊断报告编码失败", marshalErr, "diagnostic_report")
	}
	temp := path + ".tmp"
	if writeErr := os.WriteFile(temp, raw, 0o640); writeErr != nil {
		return fileFailure("诊断报告写入失败", writeErr, "diagnostic_report")
	}
	if renameErr := os.Rename(temp, path); renameErr != nil {
		return fileFailure("诊断报告原子提交失败", renameErr, "diagnostic_report")
	}
	result := ok("诊断报告生成成功", map[string]any{"path": path, "report": report}, "diagnostic_report")
	result.Artifacts = []string{path}
	return result
}

func diagnosticPath(path string) map[string]any {
	info, err := os.Stat(path)
	if err != nil {
		return map[string]any{"path": path, "exists": false, "error": err.Error()}
	}
	return map[string]any{"path": path, "exists": true, "directory": info.IsDir(), "mode": info.Mode().String()}
}

func capabilityCommandMap() map[string]bool {
	values := make(map[string]bool)
	for _, name := range []string{"sh", "cmd", "pm", "am", "settings", "svc", "dumpsys", "input", "screencap", "screenrecord", "logcat", "dmesg", "mount", "umount"} {
		values[name] = commandExists(name)
	}
	return values
}

func checkListeningPort(port string) map[string]any {
	connection, err := netDialTimeout("127.0.0.1:"+port, 500*time.Millisecond)
	if err != nil {
		return map[string]any{"listening": false, "error": err.Error()}
	}
	_ = connection.Close()
	return map[string]any{"listening": true}
}

func netDialTimeout(address string, timeout time.Duration) (io.Closer, error) {
	return netDial("tcp", address, timeout)
}

var netDial = func(network, address string, timeout time.Duration) (io.Closer, error) {
	return (&netDialer{timeout: timeout}).Dial(network, address)
}

type netDialer struct {
	timeout time.Duration
}

func (d *netDialer) Dial(network, address string) (io.Closer, error) {
	connection, err := netDialRaw(network, address, d.timeout)
	if err != nil {
		return nil, err
	}
	return connection, nil
}

var netDialRaw = func(network, address string, timeout time.Duration) (io.Closer, error) {
	return net.DialTimeout(network, address, timeout)
}
