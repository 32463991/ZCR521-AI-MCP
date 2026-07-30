package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func (m *Manager) serviceOperation(ctx context.Context, req Request) Result {
	if result := requireAndroid(); result != nil {
		return *result
	}
	action, err := actionOf(req, "list")
	if err != nil {
		return invalid(err.Error())
	}
	kind, err := argOptionalString(req.Args, "binder", "kind", "type")
	if err != nil {
		return invalid(err.Error())
	}
	kind = normalizeTool(kind)
	switch action {
	case "binder_list":
		action, kind = "list", "binder"
	case "init_status":
		action, kind = "status", "init"
	case "init_start":
		action, kind = "start", "init"
	case "init_stop":
		action, kind = "stop", "init"
	case "init_restart":
		action, kind = "restart", "init"
	case "binder_call":
		if result := requireAndroidRoot(); result != nil {
			return *result
		}
		name, nameErr := argString(req.Args, "name", "service")
		code, codeErr := argInt64(req.Args, 0, "code", "transaction")
		if nameErr != nil || strings.ContainsAny(name, " \t\r\n/\\") || codeErr != nil || code <= 0 {
			return invalid("binder_call 需要合法 service 名和正整数 transaction code")
		}
		arguments, argumentsErr := argStringSlice(req.Args, "arguments", "args")
		if argumentsErr != nil {
			if _, hasArguments := req.Args["arguments"]; hasArguments {
				return invalid(argumentsErr.Error())
			}
			if _, hasArgs := req.Args["args"]; hasArgs {
				return invalid(argumentsErr.Error())
			}
			arguments = []string{}
		}
		args := []string{"call", name, strconv.FormatInt(code, 10)}
		args = append(args, arguments...)
		return m.runAndroidRoot(ctx, commandVariant{Name: "service", Args: args, Strategy: "binder_service_call"})
	case "app_start", "app_stop":
		if result := requireAndroidRoot(); result != nil {
			return *result
		}
		component, componentErr := componentName(req.Args)
		if componentErr != nil {
			return invalid(componentErr.Error())
		}
		user, userErr := androidUser(req.Args)
		if userErr != nil || user == "all" {
			return invalid("app service 操作必须指定 current 或单个用户")
		}
		subcommand := "startservice"
		if action == "app_stop" {
			subcommand = "stopservice"
		}
		args := []string{subcommand}
		if user != "current" {
			args = append(args, "--user", user)
		}
		args = append(args, "-n", component)
		return m.runAndroidRoot(ctx, commandVariant{Name: "am", Args: args, Strategy: "am_app_service"})
	}
	switch action {
	case "list":
		switch kind {
		case "binder":
			return m.runAndroid(ctx,
				commandVariant{Name: "service", Args: []string{"list"}, Strategy: "binder_service_list"},
				commandVariant{Name: "cmd", Args: []string{"-l"}, Strategy: "cmd_service_list"},
			)
		case "init":
			result := m.runAndroid(ctx, commandVariant{Name: "getprop", Args: []string{}, Strategy: "getprop_init_services"})
			if result.Success {
				services := make(map[string]string)
				for key, value := range parseBracketGetprop(result.Stdout) {
					if strings.HasPrefix(key, "init.svc.") {
						services[strings.TrimPrefix(key, "init.svc.")] = value
					}
				}
				result.Data = services
				result.Message = "init 服务列表读取成功"
			}
			return result
		default:
			return invalid("kind 必须是 binder 或 init")
		}
	case "status":
		name, nameErr := argString(req.Args, "name", "service")
		if nameErr != nil {
			return invalid(nameErr.Error())
		}
		if kind == "init" {
			result := getProp(ctx, m, "init.svc."+name)
			if result.Success {
				result.Message = "init 服务状态读取成功"
				result.Data = map[string]string{"name": name, "state": strings.TrimSpace(result.Stdout)}
			}
			return result
		}
		return m.runAndroid(ctx, commandVariant{Name: "dumpsys", Args: []string{name}, Strategy: "dumpsys_binder_service"})
	case "start", "stop", "restart":
		if kind != "init" {
			return unsupported("Binder 服务不能通过通用接口安全启动或停止；请使用对应系统服务专用工具")
		}
		if result := requireAndroidRoot(); result != nil {
			return *result
		}
		name, nameErr := argString(req.Args, "name", "service")
		if nameErr != nil || strings.ContainsAny(name, " \t\r\n/\\") {
			return invalid("name 必须是合法 init 服务名")
		}
		commands := []string{action}
		if action == "restart" {
			commands = []string{"stop", "start"}
		}
		var result Result
		for _, operation := range commands {
			result = m.runAndroidRoot(ctx, commandVariant{Name: "setprop", Args: []string{"ctl." + operation, name}, Strategy: "init_control_property"})
			if !result.Success {
				return result
			}
		}
		readback := getProp(ctx, m, "init.svc."+name)
		expected := "running"
		if action == "stop" {
			expected = "stopped"
		}
		actual := strings.TrimSpace(readback.Stdout)
		if !readback.Success || actual != expected {
			return fail("VERIFY_FAILED", "init 服务操作后状态读回不一致", fmt.Errorf("expected %s, got %s", expected, actual), "init_service_readback")
		}
		result.Message = "init 服务状态修改并读回验证成功"
		result.Data = map[string]string{"name": name, "state": actual}
		result.Strategy += "_readback"
		return result
	default:
		return invalidAction(req.Tool, action, "app_start", "app_stop", "binder_call", "binder_list", "init_restart", "init_start", "init_status", "init_stop", "list", "restart", "start", "status", "stop")
	}
}

func parseBracketGetprop(output string) map[string]string {
	values := make(map[string]string)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		index := strings.Index(line, "]: [")
		if strings.HasPrefix(line, "[") && index > 1 && strings.HasSuffix(line, "]") {
			values[line[1:index]] = line[index+4 : len(line)-1]
		}
	}
	return values
}

func (m *Manager) systemOperation(ctx context.Context, req Request) Result {
	switch req.Tool {
	case "zcr521_property":
		return m.propertyOperation(ctx, req)
	case "zcr521_setting":
		return m.settingOperation(ctx, req)
	case "zcr521_display":
		return m.displayOperation(ctx, req)
	case "zcr521_audio":
		return m.audioOperation(ctx, req)
	case "zcr521_connectivity":
		return m.connectivityOperation(ctx, req)
	case "zcr521_locale_time":
		return m.localeTimeOperation(ctx, req)
	case "zcr521_input_method":
		return m.inputMethodOperation(ctx, req)
	case "zcr521_app_policy":
		return m.appPolicyOperation(ctx, req)
	case "zcr521_default_app":
		return m.defaultAppOperation(ctx, req)
	case "zcr521_notification":
		return m.notificationOperation(ctx, req)
	case "zcr521_accessibility":
		return m.accessibilityOperation(ctx, req)
	case "zcr521_developer":
		return m.developerOperation(ctx, req)
	default:
		return fail("UNKNOWN_TOOL", "未知系统设置工具", errors.New(req.Tool), "dispatcher")
	}
}

func (m *Manager) propertyOperation(ctx context.Context, req Request) Result {
	if result := requireAndroid(); result != nil {
		return *result
	}
	action, err := actionOf(req, "get")
	if err != nil {
		return invalid(err.Error())
	}
	switch action {
	case "list":
		result := m.runAndroid(ctx, commandVariant{Name: "getprop", Args: []string{}, Strategy: "getprop"})
		if result.Success {
			result.Data = parseBracketGetprop(result.Stdout)
			result.Message = "Android 属性列表读取成功"
		}
		return result
	case "get":
		key, keyErr := argString(req.Args, "key", "name")
		if keyErr != nil {
			return invalid(keyErr.Error())
		}
		result := getProp(ctx, m, key)
		if result.Success {
			result.Message = "Android 属性读取成功"
			result.Data = map[string]string{"key": key, "value": strings.TrimSpace(result.Stdout)}
		}
		return result
	case "set", "delete":
		if result := requireAndroidRoot(); result != nil {
			return *result
		}
		key, keyErr := argString(req.Args, "key", "name")
		if keyErr != nil || strings.ContainsAny(key, " \t\r\n=") {
			return invalid("key 必须是合法 Android 属性名")
		}
		value := ""
		if action == "set" {
			value, keyErr = argString(req.Args, "value")
			if keyErr != nil {
				return invalid(keyErr.Error())
			}
		}
		result := m.runAndroidRoot(ctx, commandVariant{Name: "setprop", Args: []string{key, value}, Strategy: "setprop"})
		if !result.Success {
			return result
		}
		readback := getProp(ctx, m, key)
		actual := strings.TrimSpace(readback.Stdout)
		if !readback.Success || actual != value {
			return fail("VERIFY_FAILED", "Android 属性写入后读回不一致；只读属性或属性上下文可能拒绝修改", fmt.Errorf("expected %q, got %q", value, actual), "setprop_readback")
		}
		result.Message = "Android 属性修改并读回验证成功"
		result.Data = map[string]string{"key": key, "value": actual}
		result.Strategy += "_readback"
		return result
	default:
		return invalidAction(req.Tool, action, "delete", "get", "list", "set")
	}
}

func (m *Manager) settingOperation(ctx context.Context, req Request) Result {
	if result := requireAndroid(); result != nil {
		return *result
	}
	action, err := actionOf(req, "get")
	if err != nil {
		return invalid(err.Error())
	}
	namespaceRaw, err := argOptionalString(req.Args, "global", "namespace", "table")
	if err != nil {
		return invalid(err.Error())
	}
	namespace, err := validateSettingsNamespace(namespaceRaw)
	if err != nil {
		return invalid(err.Error())
	}
	user, err := androidUser(req.Args)
	if err != nil || user == "all" {
		return invalid("settings user 必须是 current 或单个用户")
	}
	switch action {
	case "list":
		args := []string{}
		if user != "current" {
			args = append(args, "--user", user)
		}
		args = append(args, "list", namespace)
		result := m.runAndroid(ctx,
			commandVariant{Name: "settings", Args: args, Strategy: "settings_cli"},
			commandVariant{Name: "cmd", Args: append([]string{"settings"}, args...), Strategy: "cmd_settings"},
		)
		if result.Success {
			result.Data = parseKeyValueLines(result.Stdout)
			result.Message = "设置列表读取成功"
		}
		return result
	case "get":
		key, keyErr := argString(req.Args, "key")
		if keyErr != nil {
			return invalid(keyErr.Error())
		}
		result := settingsGet(ctx, m, namespace, key, user)
		if result.Success {
			result.Data = map[string]string{"namespace": namespace, "key": key, "value": strings.TrimSpace(result.Stdout), "user": user}
			result.Message = "设置读取成功"
		}
		return result
	case "put", "set":
		key, keyErr := argString(req.Args, "key")
		value, valueErr := argString(req.Args, "value")
		if keyErr != nil || valueErr != nil {
			return invalid("put/set 需要 key 和字符串 value")
		}
		return settingsPutVerified(ctx, m, namespace, key, value, user)
	case "delete":
		key, keyErr := argString(req.Args, "key")
		if keyErr != nil {
			return invalid(keyErr.Error())
		}
		return settingsDeleteVerified(ctx, m, namespace, key, user)
	default:
		return invalidAction(req.Tool, action, "delete", "get", "list", "put", "set")
	}
}

func (m *Manager) displayOperation(ctx context.Context, req Request) Result {
	if result := requireAndroid(); result != nil {
		return *result
	}
	action, err := actionOf(req, "get")
	if err != nil {
		return invalid(err.Error())
	}
	user, err := androidUser(req.Args)
	if err != nil || user == "all" {
		return invalid("display user 必须是 current 或单个用户")
	}
	switch action {
	case "get", "status":
		result := m.runAndroid(ctx, commandVariant{Name: "dumpsys", Args: []string{"display"}, Strategy: "dumpsys_display"})
		if result.Success {
			result.Data = map[string]any{
				"raw":        result.Stdout,
				"brightness": strings.TrimSpace(settingsGet(ctx, m, "system", "screen_brightness", user).Stdout),
				"timeoutMs":  strings.TrimSpace(settingsGet(ctx, m, "system", "screen_off_timeout", user).Stdout),
				"rotation":   strings.TrimSpace(settingsGet(ctx, m, "system", "user_rotation", user).Stdout),
			}
			result.Message = "显示状态读取成功"
		}
		return result
	case "brightness":
		value, parseErr := argInt64(req.Args, -1, "value")
		if parseErr != nil || value < 0 || value > 255 {
			return invalid("亮度 value 必须在 0 到 255 之间")
		}
		return settingsPutVerified(ctx, m, "system", "screen_brightness", strconv.FormatInt(value, 10), user)
	case "timeout":
		value, parseErr := argInt64(req.Args, -1, "value", "milliseconds")
		if parseErr != nil || value < 0 {
			return invalid("屏幕超时必须是非负毫秒")
		}
		return settingsPutVerified(ctx, m, "system", "screen_off_timeout", strconv.FormatInt(value, 10), user)
	case "rotation":
		value, parseErr := argInt64(req.Args, -1, "value")
		if parseErr != nil || value < 0 || value > 3 {
			return invalid("rotation value 必须在 0 到 3 之间")
		}
		auto, _ := argBool(req.Args, "auto", false)
		autoValue := "0"
		if auto {
			autoValue = "1"
		}
		if result := settingsPutVerified(ctx, m, "system", "accelerometer_rotation", autoValue, user); !result.Success {
			return result
		}
		return settingsPutVerified(ctx, m, "system", "user_rotation", strconv.FormatInt(value, 10), user)
	case "dark_mode":
		mode, parseErr := argString(req.Args, "mode", "value")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		mode = strings.ToLower(mode)
		if mode != "yes" && mode != "no" && mode != "auto" && mode != "custom" {
			return invalid("mode 必须是 yes、no、auto 或 custom")
		}
		result := m.runAndroidRoot(ctx, commandVariant{Name: "cmd", Args: []string{"uimode", "night", mode}, Strategy: "cmd_uimode"})
		if !result.Success {
			return result
		}
		readback := m.runAndroid(ctx, commandVariant{Name: "cmd", Args: []string{"uimode", "night"}, Strategy: "cmd_uimode"})
		if !readback.Success || !strings.Contains(strings.ToLower(readback.Stdout), mode) {
			return fail("VERIFY_FAILED", "深色模式修改后读回不一致", errors.New(readback.Stdout+" "+readback.Stderr), "cmd_uimode_readback")
		}
		result.Data = map[string]string{"mode": mode, "readback": strings.TrimSpace(readback.Stdout)}
		result.Message = "深色模式修改并读回验证成功"
		return result
	case "animation":
		scale, parseErr := argOptionalString(req.Args, "1", "scale", "value")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		if parsed, parseFloatErr := strconv.ParseFloat(scale, 64); parseFloatErr != nil || parsed < 0 || parsed > 10 {
			return invalid("动画 scale 必须是 0 到 10 的数字")
		}
		for _, key := range []string{"window_animation_scale", "transition_animation_scale", "animator_duration_scale"} {
			if result := settingsPutVerified(ctx, m, "global", key, scale, user); !result.Success {
				return result
			}
		}
		return ok("三项系统动画速度修改并读回验证成功", map[string]string{"scale": scale}, "settings_animation_readback")
	case "size", "density":
		reset, _ := argBool(req.Args, "reset", false)
		args := []string{action}
		if reset {
			args = append(args, "reset")
		} else {
			value, parseErr := argString(req.Args, "value")
			if parseErr != nil {
				return invalid(parseErr.Error())
			}
			args = append(args, value)
		}
		result := m.runAndroidRoot(ctx, commandVariant{Name: "wm", Args: args, Strategy: "wm_display"})
		if !result.Success {
			return result
		}
		readback := m.runAndroid(ctx, commandVariant{Name: "wm", Args: []string{action}, Strategy: "wm_display"})
		if !readback.Success {
			return fail("VERIFY_FAILED", "显示参数修改后无法读回", errors.New(readback.Error), "wm_readback")
		}
		result.Data = map[string]string{"readback": strings.TrimSpace(readback.Stdout)}
		result.Message = "显示参数修改并读回成功"
		return result
	default:
		return invalidAction(req.Tool, action, "animation", "brightness", "dark_mode", "density", "get", "rotation", "size", "status", "timeout")
	}
}

func (m *Manager) audioOperation(ctx context.Context, req Request) Result {
	if result := requireAndroid(); result != nil {
		return *result
	}
	action, err := actionOf(req, "get")
	if err != nil {
		return invalid(err.Error())
	}
	switch action {
	case "get", "status":
		return m.runAndroid(ctx, commandVariant{Name: "dumpsys", Args: []string{"audio"}, Strategy: "dumpsys_audio"})
	case "set_volume":
		if result := requireAndroidRoot(); result != nil {
			return *result
		}
		stream, parseErr := argInt64(req.Args, 3, "stream")
		level, levelErr := argInt64(req.Args, -1, "level", "value")
		if parseErr != nil || stream < 0 || stream > 10 || levelErr != nil || level < 0 {
			return invalid("stream 必须在 0 到 10，level 必须是非负整数")
		}
		result := m.runAndroidRoot(ctx,
			commandVariant{Name: "cmd", Args: []string{"media_session", "volume", "--stream", strconv.FormatInt(stream, 10), "--set", strconv.FormatInt(level, 10)}, Strategy: "cmd_media_session"},
			commandVariant{Name: "media", Args: []string{"volume", "--stream", strconv.FormatInt(stream, 10), "--set", strconv.FormatInt(level, 10)}, Strategy: "media_volume"},
		)
		if !result.Success {
			return result
		}
		readback := m.runAndroid(ctx,
			commandVariant{Name: "cmd", Args: []string{"media_session", "volume", "--stream", strconv.FormatInt(stream, 10), "--get"}, Strategy: "cmd_media_session"},
			commandVariant{Name: "dumpsys", Args: []string{"audio"}, Strategy: "dumpsys_audio"},
		)
		if !readback.Success {
			return fail("VERIFY_FAILED", "音量修改后无法读回", errors.New(readback.Error), "audio_readback")
		}
		result.Data = map[string]any{"stream": stream, "requestedLevel": level, "readback": readback.Stdout}
		result.Message = "音量修改并完成系统读回"
		return result
	case "mute":
		if result := requireAndroidRoot(); result != nil {
			return *result
		}
		stream, streamErr := argInt64(req.Args, 3, "stream")
		muted, mutedErr := argBool(req.Args, "muted", true)
		if streamErr != nil || stream < 0 || stream > 10 || mutedErr != nil {
			return invalid("mute 需要 0 到 10 的 stream 和布尔 muted")
		}
		adjustment := "unmute"
		if muted {
			adjustment = "mute"
		}
		return m.runAndroidRoot(ctx,
			commandVariant{Name: "cmd", Args: []string{"media_session", "volume", "--stream", strconv.FormatInt(stream, 10), "--adj", adjustment}, Strategy: "cmd_media_session"},
			commandVariant{Name: "media", Args: []string{"volume", "--stream", strconv.FormatInt(stream, 10), "--adj", adjustment}, Strategy: "media_volume"},
		)
	case "route":
		mode, parseErr := argOptionalString(req.Args, "", "mode", "value")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		mode = normalizeTool(mode)
		if mode != "" && mode != "get" && mode != "status" {
			return unsupported("Android 26-36 没有跨版本稳定的通用音频路由写接口；route 当前仅安全读取实际 AudioService 路由")
		}
		result := m.runAndroid(ctx, commandVariant{Name: "dumpsys", Args: []string{"audio"}, Strategy: "dumpsys_audio_route"})
		if result.Success {
			result.Message = "音频路由状态读取成功"
			result.Data = map[string]any{"raw": result.Stdout, "writable": false}
		}
		return result
	default:
		return invalidAction(req.Tool, action, "get", "mute", "route", "set_volume", "status")
	}
}

func (m *Manager) connectivityOperation(ctx context.Context, req Request) Result {
	if result := requireAndroid(); result != nil {
		return *result
	}
	action, err := actionOf(req, "status")
	if err != nil {
		return invalid(err.Error())
	}
	switch action {
	case "status":
		return m.runAndroid(ctx, commandVariant{Name: "dumpsys", Args: []string{"connectivity"}, Strategy: "dumpsys_connectivity"})
	case "wifi", "mobile_data":
		enabled, parseErr := argBool(req.Args, "enabled", false)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		service := "wifi"
		if action == "mobile_data" {
			service = "data"
		}
		state := "disable"
		if enabled {
			state = "enable"
		}
		result := m.runAndroidRoot(ctx, commandVariant{Name: "svc", Args: []string{service, state}, Strategy: "svc_connectivity"})
		if !result.Success {
			return result
		}
		if action == "wifi" {
			readback := m.runAndroid(ctx,
				commandVariant{Name: "cmd", Args: []string{"wifi", "status"}, Strategy: "cmd_wifi"},
				commandVariant{Name: "dumpsys", Args: []string{"wifi"}, Strategy: "dumpsys_wifi"},
			)
			if !readback.Success {
				return fail("VERIFY_FAILED", "Wi-Fi 修改后无法读回状态", errors.New(readback.Error), "wifi_readback")
			}
			result.Data = map[string]any{"enabled": enabled, "readback": readback.Stdout}
		} else {
			readback := settingsGet(ctx, m, "global", "mobile_data", "current")
			result.Data = map[string]any{"enabled": enabled, "readback": strings.TrimSpace(readback.Stdout), "note": "多 SIM 厂商 ROM 可能使用每订阅设置"}
		}
		result.Message = "连接状态修改并完成读回"
		return result
	case "airplane":
		enabled, parseErr := argBool(req.Args, "enabled", false)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		state := "disable"
		value := "0"
		if enabled {
			state, value = "enable", "1"
		}
		result := m.runAndroidRoot(ctx, commandVariant{Name: "cmd", Args: []string{"connectivity", "airplane-mode", state}, Strategy: "cmd_connectivity"})
		if !result.Success {
			result = settingsPutVerified(ctx, m, "global", "airplane_mode_on", value, "current")
			if !result.Success {
				return result
			}
			broadcast := m.runAndroidRoot(ctx, commandVariant{Name: "am", Args: []string{"broadcast", "-a", "android.intent.action.AIRPLANE_MODE", "--ez", "state", strconv.FormatBool(enabled)}, Strategy: "am_airplane_broadcast"})
			if !broadcast.Success {
				return broadcast
			}
		}
		readback := settingsGet(ctx, m, "global", "airplane_mode_on", "current")
		if !readback.Success || strings.TrimSpace(readback.Stdout) != value {
			return fail("VERIFY_FAILED", "飞行模式修改后读回不一致", errors.New(readback.Stdout+" "+readback.Stderr), "airplane_readback")
		}
		result.Data = map[string]any{"enabled": enabled}
		result.Message = "飞行模式修改并读回验证成功"
		return result
	case "bluetooth":
		enabled, parseErr := argBool(req.Args, "enabled", false)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		state := "disable"
		if enabled {
			state = "enable"
		}
		result := m.runAndroidRoot(ctx, commandVariant{Name: "cmd", Args: []string{"bluetooth_manager", state}, Strategy: "cmd_bluetooth_manager"})
		if !result.Success {
			return result
		}
		readback := m.runAndroid(ctx, commandVariant{Name: "dumpsys", Args: []string{"bluetooth_manager"}, Strategy: "dumpsys_bluetooth_manager"})
		if !readback.Success {
			return fail("VERIFY_FAILED", "蓝牙修改后无法读回状态", errors.New(readback.Error), "bluetooth_readback")
		}
		result.Data = map[string]any{"enabled": enabled, "readback": readback.Stdout}
		result.Message = "蓝牙状态修改并完成读回"
		return result
	case "nfc":
		enabled, parseErr := argBool(req.Args, "enabled", false)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		state := "disable"
		if enabled {
			state = "enable"
		}
		result := m.runAndroidRoot(ctx, commandVariant{Name: "svc", Args: []string{"nfc", state}, Strategy: "svc_nfc"})
		if !result.Success {
			return result
		}
		readback := m.runAndroid(ctx, commandVariant{Name: "dumpsys", Args: []string{"nfc"}, Strategy: "dumpsys_nfc"})
		result.Data = map[string]any{"enabled": enabled, "readback": readback.Stdout}
		return result
	case "proxy_get":
		return settingsGet(ctx, m, "global", "http_proxy", "current")
	case "proxy_set":
		host, hostErr := argString(req.Args, "host")
		port, portErr := argInt64(req.Args, 0, "port")
		if hostErr != nil || portErr != nil || port <= 0 || port > 65535 {
			return invalid("proxy_set 需要 host 和 1..65535 的 port")
		}
		return settingsPutVerified(ctx, m, "global", "http_proxy", fmt.Sprintf("%s:%d", host, port), "current")
	case "proxy_clear":
		return settingsPutVerified(ctx, m, "global", "http_proxy", ":0", "current")
	case "private_dns":
		mode, modeErr := argString(req.Args, "mode")
		if modeErr != nil || mode != "off" && mode != "opportunistic" && mode != "hostname" {
			return invalid("private_dns mode 必须是 off、opportunistic 或 hostname")
		}
		if result := settingsPutVerified(ctx, m, "global", "private_dns_mode", mode, "current"); !result.Success {
			return result
		}
		if mode == "hostname" {
			host, hostErr := argString(req.Args, "hostname")
			if hostErr != nil {
				return invalid(hostErr.Error())
			}
			return settingsPutVerified(ctx, m, "global", "private_dns_specifier", host, "current")
		}
		return ok("私有 DNS 模式修改并读回验证成功", map[string]string{"mode": mode}, "settings_private_dns")
	default:
		return invalidAction(req.Tool, action, "airplane", "bluetooth", "mobile_data", "nfc", "private_dns", "proxy_clear", "proxy_get", "proxy_set", "status", "wifi")
	}
}

func (m *Manager) localeTimeOperation(ctx context.Context, req Request) Result {
	if result := requireAndroid(); result != nil {
		return *result
	}
	action, err := actionOf(req, "get")
	if err != nil {
		return invalid(err.Error())
	}
	switch action {
	case "get":
		return ok("语言和时间设置读取成功", map[string]any{
			"locale":       strings.TrimSpace(getProp(ctx, m, "persist.sys.locale").Stdout),
			"timezone":     strings.TrimSpace(getProp(ctx, m, "persist.sys.timezone").Stdout),
			"epochSeconds": time.Now().Unix(),
		}, "getprop_time")
	case "timezone":
		timezone, parseErr := argString(req.Args, "timezone", "value")
		if parseErr != nil || strings.ContainsAny(timezone, "\x00\r\n ") {
			return invalid("timezone 必须是 IANA 时区名")
		}
		result := m.runAndroidRoot(ctx,
			commandVariant{Name: "cmd", Args: []string{"alarm", "set-timezone", timezone}, Strategy: "cmd_alarm_timezone"},
			commandVariant{Name: "setprop", Args: []string{"persist.sys.timezone", timezone}, Strategy: "setprop_timezone"},
		)
		if !result.Success {
			return result
		}
		actual := strings.TrimSpace(getProp(ctx, m, "persist.sys.timezone").Stdout)
		if actual != timezone {
			return fail("VERIFY_FAILED", "时区修改后读回不一致", fmt.Errorf("expected %s, got %s", timezone, actual), "timezone_readback")
		}
		result.Data = map[string]string{"timezone": actual}
		result.Message = "时区修改并读回验证成功"
		return result
	case "time_format":
		value, parseErr := argOptionalString(req.Args, "", "format", "value")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		if value == "" {
			return settingsGet(ctx, m, "system", "time_12_24", "current")
		}
		value = strings.TrimSpace(strings.ToLower(value))
		switch value {
		case "12", "12h", "h12":
			value = "12"
		case "24", "24h", "h24":
			value = "24"
		default:
			return invalid("time_format 必须是 12 或 24")
		}
		return settingsPutVerified(ctx, m, "system", "time_12_24", value, "current")
	case "date_time":
		value, parseErr := argString(req.Args, "value", "time")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		parsed, parseErr := time.Parse(time.RFC3339, value)
		if parseErr != nil {
			return invalid("value 必须是 RFC3339 时间")
		}
		result := m.runAndroidRoot(ctx,
			commandVariant{Name: "date", Args: []string{"-s", "@" + strconv.FormatInt(parsed.Unix(), 10)}, Strategy: "toybox_date"},
			commandVariant{Name: "toybox", Args: []string{"date", "-s", "@" + strconv.FormatInt(parsed.Unix(), 10)}, Strategy: "toybox_date"},
		)
		if !result.Success {
			return result
		}
		actual := time.Now().Unix()
		if delta := actual - parsed.Unix(); delta < -5 || delta > 5 {
			return fail("VERIFY_FAILED", "系统时间修改后读回偏差超过 5 秒", fmt.Errorf("expected %d, got %d", parsed.Unix(), actual), "clock_readback")
		}
		result.Data = map[string]any{"epochSeconds": actual}
		result.Message = "系统时间修改并读回验证成功"
		return result
	case "locale", "language":
		value, parseErr := argString(req.Args, "locale", "value")
		if parseErr != nil || strings.ContainsAny(value, "\x00\r\n ") {
			return invalid("locale 必须是合法区域标识，例如 zh-CN")
		}
		result := m.runAndroidRoot(ctx, commandVariant{Name: "setprop", Args: []string{"persist.sys.locale", value}, Strategy: "setprop_locale"})
		if !result.Success {
			return result
		}
		actual := strings.TrimSpace(getProp(ctx, m, "persist.sys.locale").Stdout)
		if actual != value {
			return fail("VERIFY_FAILED", "区域设置修改后读回不一致", fmt.Errorf("expected %s, got %s", value, actual), "locale_readback")
		}
		result.Data = map[string]string{"locale": actual}
		result.Message = "区域设置已写入；部分系统需重启 Android framework 才完全生效"
		result.RebootRequired = true
		return result
	default:
		return invalidAction(req.Tool, action, "date_time", "get", "language", "locale", "time_format", "timezone")
	}
}

func (m *Manager) inputMethodOperation(ctx context.Context, req Request) Result {
	if result := requireAndroid(); result != nil {
		return *result
	}
	action, err := actionOf(req, "list")
	if err != nil {
		return invalid(err.Error())
	}
	switch action {
	case "list":
		return m.runAndroid(ctx, commandVariant{Name: "ime", Args: []string{"list", "-a"}, Strategy: "ime_cli"})
	case "current":
		return settingsGet(ctx, m, "secure", "default_input_method", "current")
	case "enable", "disable", "set":
		if result := requireAndroidRoot(); result != nil {
			return *result
		}
		id, parseErr := argString(req.Args, "id", "component")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		result := m.runAndroidRoot(ctx, commandVariant{Name: "ime", Args: []string{action, id}, Strategy: "ime_cli"})
		if !result.Success {
			return result
		}
		if action == "set" {
			actual := settingsGet(ctx, m, "secure", "default_input_method", "current")
			if !actual.Success || strings.TrimSpace(actual.Stdout) != id {
				return fail("VERIFY_FAILED", "输入法切换后读回不一致", errors.New(actual.Stdout+" "+actual.Stderr), "ime_readback")
			}
		}
		result.Data = map[string]string{"id": id, "action": action}
		result.Message = "输入法操作成功并完成状态检查"
		return result
	default:
		return invalidAction(req.Tool, action, "current", "disable", "enable", "list", "set")
	}
}

func (m *Manager) appPolicyOperation(ctx context.Context, req Request) Result {
	if result := requireAndroid(); result != nil {
		return *result
	}
	action, err := actionOf(req, "battery_list")
	if err != nil {
		return invalid(err.Error())
	}
	if action == "battery_optimization" {
		if _, hasPackage := req.Args["package"]; !hasPackage {
			action = "battery_list"
		} else {
			optimized, parseErr := argBool(req.Args, "optimized", true)
			if excluded, exists := req.Args["excluded"]; exists {
				excludedValue, excludedErr := argBool(map[string]any{"excluded": excluded}, "excluded", false)
				if excludedErr != nil {
					return invalid(excludedErr.Error())
				}
				optimized = !excludedValue
			}
			if parseErr != nil {
				return invalid(parseErr.Error())
			}
			action = "battery_disallow"
			if !optimized {
				action = "battery_allow"
			}
		}
	}
	switch action {
	case "get":
		pkg, _ := argOptionalString(req.Args, "", "package", "packageName")
		data := map[string]any{
			"batteryOptimizationWhitelist": commandData(m.runAndroid(ctx, commandVariant{Name: "dumpsys", Args: []string{"deviceidle", "whitelist"}, Strategy: "deviceidle_whitelist"})),
		}
		if pkg != "" {
			data["standbyBucket"] = commandData(m.runAndroid(ctx, commandVariant{Name: "am", Args: []string{"get-standby-bucket", pkg}, Strategy: "am_standby_bucket"}))
		}
		return ok("应用策略读取成功", data, "app_policy_readback")
	case "standby_bucket":
		pkg, parseErr := packageName(req.Args)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		bucket, bucketErr := argOptionalString(req.Args, "", "bucket", "value")
		if bucketErr != nil {
			return invalid(bucketErr.Error())
		}
		if bucket == "" {
			return m.runAndroid(ctx, commandVariant{Name: "am", Args: []string{"get-standby-bucket", pkg}, Strategy: "am_standby_bucket"})
		}
		switch bucket {
		case "active", "working_set", "frequent", "rare", "restricted", "never", "10", "20", "30", "40", "45", "50":
		default:
			return invalid("bucket 必须是合法 standby bucket 名称或值")
		}
		result := m.runAndroidRoot(ctx, commandVariant{Name: "am", Args: []string{"set-standby-bucket", pkg, bucket}, Strategy: "am_standby_bucket"})
		if !result.Success {
			return result
		}
		result.Data = commandData(m.runAndroid(ctx, commandVariant{Name: "am", Args: []string{"get-standby-bucket", pkg}, Strategy: "am_standby_bucket"}))
		return result
	case "battery_list":
		return m.runAndroid(ctx, commandVariant{Name: "dumpsys", Args: []string{"deviceidle", "whitelist"}, Strategy: "deviceidle_whitelist"})
	case "battery_allow", "battery_disallow":
		pkg, parseErr := packageName(req.Args)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		prefix := "+"
		if action == "battery_disallow" {
			prefix = "-"
		}
		result := m.runAndroidRoot(ctx, commandVariant{Name: "dumpsys", Args: []string{"deviceidle", "whitelist", prefix + pkg}, Strategy: "deviceidle_whitelist"})
		if !result.Success {
			return result
		}
		readback := m.runAndroid(ctx, commandVariant{Name: "dumpsys", Args: []string{"deviceidle", "whitelist"}, Strategy: "deviceidle_whitelist"})
		expected := action == "battery_allow"
		actual := strings.Contains(readback.Stdout, pkg)
		if !readback.Success || actual != expected {
			return fail("VERIFY_FAILED", "电池优化白名单修改后读回不一致", errors.New(readback.Stdout+" "+readback.Stderr), "deviceidle_readback")
		}
		result.Data = map[string]any{"package": pkg, "allowed": actual}
		result.Message = "电池优化策略修改并读回验证成功"
		return result
	case "background":
		pkg, parseErr := packageName(req.Args)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		allowed, parseErr := argBool(req.Args, "allowed", true)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		mode := "ignore"
		if allowed {
			mode = "allow"
		}
		result := m.runAndroidRoot(ctx, commandVariant{Name: "cmd", Args: []string{"appops", "set", pkg, "RUN_ANY_IN_BACKGROUND", mode}, Strategy: "cmd_appops"})
		if !result.Success {
			return result
		}
		readback := m.runAndroid(ctx, commandVariant{Name: "cmd", Args: []string{"appops", "get", pkg, "RUN_ANY_IN_BACKGROUND"}, Strategy: "cmd_appops"})
		if !readback.Success || !strings.Contains(strings.ToLower(readback.Stdout), mode) {
			return fail("VERIFY_FAILED", "后台策略修改后读回不一致", errors.New(readback.Stdout+" "+readback.Stderr), "appops_readback")
		}
		result.Data = map[string]any{"package": pkg, "allowed": allowed, "readback": readback.Stdout}
		result.Message = "应用后台策略修改并读回验证成功"
		return result
	default:
		return invalidAction(req.Tool, action, "background", "battery_allow", "battery_disallow", "battery_list", "battery_optimization", "get", "standby_bucket")
	}
}

func (m *Manager) defaultAppOperation(ctx context.Context, req Request) Result {
	if result := requireAndroid(); result != nil {
		return *result
	}
	action, err := actionOf(req, "get")
	if err != nil {
		return invalid(err.Error())
	}
	role, err := argOptionalString(req.Args, "android.app.role.HOME", "role")
	if err != nil {
		return invalid(err.Error())
	}
	user, err := androidUser(req.Args)
	if err != nil || user == "all" {
		return invalid("默认应用 user 必须是 current 或单个用户")
	}
	switch action {
	case "get":
		args := []string{"role", "get-role-holders", role}
		if user != "current" {
			args = append(args, "--user", user)
		}
		return m.runAndroid(ctx, commandVariant{Name: "cmd", Args: args, Strategy: "cmd_role"})
	case "set":
		pkg, parseErr := packageName(req.Args)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		args := []string{"role", "add-role-holder", role, pkg, "0"}
		if user != "current" {
			args = append(args, "--user", user)
		}
		result := m.runAndroidRoot(ctx,
			commandVariant{Name: "cmd", Args: args, Strategy: "cmd_role"},
			commandVariant{Name: "cmd", Args: []string{"package", "set-home-activity", pkg}, Strategy: "cmd_package_home"},
		)
		if !result.Success {
			return result
		}
		readback := m.runAndroid(ctx, commandVariant{Name: "cmd", Args: []string{"role", "get-role-holders", role}, Strategy: "cmd_role"})
		if !readback.Success || !strings.Contains(readback.Stdout, pkg) {
			return fail("VERIFY_FAILED", "默认应用修改后读回不一致", errors.New(readback.Stdout+" "+readback.Stderr), "default_app_readback")
		}
		result.Data = map[string]string{"role": role, "package": pkg}
		result.Message = "默认应用修改并读回验证成功"
		return result
	case "clear":
		args := []string{"role", "clear-role-holders", role, "0"}
		if user != "current" {
			args = append(args, "--user", user)
		}
		result := m.runAndroidRoot(ctx, commandVariant{Name: "cmd", Args: args, Strategy: "cmd_role_clear"})
		if !result.Success {
			return result
		}
		readbackArgs := []string{"role", "get-role-holders", role}
		if user != "current" {
			readbackArgs = append(readbackArgs, "--user", user)
		}
		readback := m.runAndroid(ctx, commandVariant{Name: "cmd", Args: readbackArgs, Strategy: "cmd_role"})
		result.Data = map[string]any{"role": role, "holders": strings.Fields(readback.Stdout)}
		return result
	default:
		return invalidAction(req.Tool, action, "clear", "get", "set")
	}
}

func (m *Manager) notificationOperation(ctx context.Context, req Request) Result {
	if result := requireAndroid(); result != nil {
		return *result
	}
	action, err := actionOf(req, "status")
	if err != nil {
		return invalid(err.Error())
	}
	if action == "get" {
		action = "status"
	}
	if action == "listener" {
		enabled, parseErr := argBool(req.Args, "enabled", true)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		action = "disallow_listener"
		if enabled {
			action = "allow_listener"
		}
	}
	switch action {
	case "status", "list":
		return m.runAndroid(ctx, commandVariant{Name: "dumpsys", Args: []string{"notification", "--noredact"}, Strategy: "dumpsys_notification"})
	case "allow", "deny":
		pkg, parseErr := packageName(req.Args)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		permissionAction := "revoke"
		appOpMode := "ignore"
		if action == "allow" {
			permissionAction = "grant"
			appOpMode = "allow"
		}
		result := m.runAndroidRoot(ctx, commandVariant{Name: "pm", Args: []string{permissionAction, pkg, "android.permission.POST_NOTIFICATIONS"}, Strategy: "pm_notification_permission"})
		if !result.Success {
			result = m.runAndroidRoot(ctx, commandVariant{Name: "cmd", Args: []string{"appops", "set", pkg, "POST_NOTIFICATION", appOpMode}, Strategy: "cmd_notification_appops"})
		}
		return result
	case "allow_listener", "disallow_listener":
		component, parseErr := componentName(req.Args)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		subcommand := "allow_listener"
		if action == "disallow_listener" {
			subcommand = "disallow_listener"
		}
		result := m.runAndroidRoot(ctx, commandVariant{Name: "cmd", Args: []string{"notification", subcommand, component}, Strategy: "cmd_notification"})
		if !result.Success {
			return result
		}
		readback := settingsGet(ctx, m, "secure", "enabled_notification_listeners", "current")
		expected := action == "allow_listener"
		actual := strings.Contains(readback.Stdout, component)
		if !readback.Success || actual != expected {
			return fail("VERIFY_FAILED", "通知监听器修改后读回不一致", errors.New(readback.Stdout+" "+readback.Stderr), "notification_listener_readback")
		}
		result.Data = map[string]any{"component": component, "allowed": actual}
		result.Message = "通知监听器状态修改并读回验证成功"
		return result
	default:
		return invalidAction(req.Tool, action, "allow", "allow_listener", "deny", "disallow_listener", "get", "list", "listener", "status")
	}
}

func (m *Manager) accessibilityOperation(ctx context.Context, req Request) Result {
	if result := requireAndroid(); result != nil {
		return *result
	}
	action, err := actionOf(req, "get")
	if err != nil {
		return invalid(err.Error())
	}
	user, err := androidUser(req.Args)
	if err != nil || user == "all" {
		return invalid("accessibility user 必须是 current 或单个用户")
	}
	current := settingsGet(ctx, m, "secure", "enabled_accessibility_services", user)
	switch action {
	case "get":
		enabledFlag := settingsGet(ctx, m, "secure", "accessibility_enabled", user)
		return ok("无障碍服务状态读取成功", map[string]any{
			"services": strings.TrimSpace(current.Stdout),
			"enabled":  strings.TrimSpace(enabledFlag.Stdout) == "1",
		}, "settings_accessibility")
	case "enable", "disable":
		component, parseErr := componentName(req.Args)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		services := splitColonList(strings.TrimSpace(current.Stdout))
		if action == "enable" {
			services[component] = true
		} else {
			delete(services, component)
		}
		value := joinColonList(services)
		if result := settingsPutVerified(ctx, m, "secure", "enabled_accessibility_services", value, user); !result.Success {
			return result
		}
		flag := "0"
		if len(services) > 0 {
			flag = "1"
		}
		result := settingsPutVerified(ctx, m, "secure", "accessibility_enabled", flag, user)
		if result.Success {
			result.Data = map[string]any{"component": component, "enabled": action == "enable", "services": value}
		}
		return result
	default:
		return invalidAction(req.Tool, action, "disable", "enable", "get")
	}
}

func splitColonList(value string) map[string]bool {
	out := make(map[string]bool)
	if value == "" || value == "null" {
		return out
	}
	for _, item := range strings.Split(value, ":") {
		if strings.TrimSpace(item) != "" {
			out[item] = true
		}
	}
	return out
}

func joinColonList(values map[string]bool) string {
	items := make([]string, 0, len(values))
	for item := range values {
		items = append(items, item)
	}
	sortStrings(items)
	return strings.Join(items, ":")
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func (m *Manager) developerOperation(ctx context.Context, req Request) Result {
	if result := requireAndroid(); result != nil {
		return *result
	}
	action, err := actionOf(req, "get")
	if err != nil {
		return invalid(err.Error())
	}
	keys := map[string]string{
		"developer_options": "development_settings_enabled",
		"usb_debugging":     "adb_enabled",
		"stay_awake":        "stay_on_while_plugged_in",
	}
	switch action {
	case "get":
		data := make(map[string]string)
		for name, key := range keys {
			data[name] = strings.TrimSpace(settingsGet(ctx, m, "global", key, "current").Stdout)
		}
		return ok("开发者设置读取成功", data, "settings_developer")
	case "adb":
		enabled, parseErr := requiredEnabled(req.Args)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		value := "0"
		if enabled {
			value = "1"
		}
		result := settingsPutVerified(ctx, m, "global", "adb_enabled", value, "current")
		if result.Success {
			result.Message = "ADB 调试状态修改并读回验证成功"
			result.Data = map[string]any{"enabled": enabled, "value": value}
		}
		return result
	case "stay_awake":
		enabled, parseErr := requiredEnabled(req.Args)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		value := "0"
		if enabled {
			value = "7"
			if raw, exists := req.Args["value"]; exists {
				parsed, valueErr := argInt64(map[string]any{"value": raw}, 7, "value")
				if valueErr != nil || parsed < 1 || parsed > 7 {
					return invalid("stay_awake value 必须是 1..7 的电源类型位掩码")
				}
				value = strconv.FormatInt(parsed, 10)
			}
		}
		result := settingsPutVerified(ctx, m, "global", "stay_on_while_plugged_in", value, "current")
		if result.Success {
			result.Message = "充电时保持唤醒设置修改并读回验证成功"
			result.Data = map[string]any{"enabled": enabled, "value": value}
		}
		return result
	case "animation":
		scale, parseErr := argOptionalString(req.Args, "", "scale")
		if scale == "" {
			if raw, exists := req.Args["value"]; exists {
				scale = fmt.Sprint(raw)
			}
		}
		if parseErr != nil || scale == "" {
			return invalid("animation 需要 scale，例如 0、0.5 或 1")
		}
		parsed, parseFloatErr := strconv.ParseFloat(scale, 64)
		if parseFloatErr != nil || parsed < 0 || parsed > 10 {
			return invalid("animation scale 必须是 0..10 的数字")
		}
		normalized := strconv.FormatFloat(parsed, 'f', -1, 64)
		for _, key := range []string{
			"window_animation_scale",
			"transition_animation_scale",
			"animator_duration_scale",
		} {
			if result := settingsPutVerified(ctx, m, "global", key, normalized, "current"); !result.Success {
				return result
			}
		}
		return ok(
			"三项系统动画缩放修改并读回验证成功",
			map[string]string{"scale": normalized},
			"settings_animation_readback",
		)
	case "mock_location":
		enabled, parseErr := requiredEnabled(req.Args)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		packageValue, parseErr := argOptionalString(req.Args, "", "name")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		if packageValue == "" {
			if raw, exists := req.Args["value"]; exists {
				packageValue, _ = raw.(string)
			}
		}
		pkg, packageErr := packageName(map[string]any{"package": packageValue})
		if packageErr != nil {
			return invalid("mock_location 需要在 name 或 value 中提供合法包名")
		}
		mode := "ignore"
		if enabled {
			mode = "allow"
		}
		result := m.runAndroidRoot(ctx,
			commandVariant{Name: "cmd", Args: []string{"appops", "set", pkg, "android:mock_location", mode}, Strategy: "cmd_appops_mock_location"},
			commandVariant{Name: "appops", Args: []string{"set", pkg, "android:mock_location", mode}, Strategy: "appops_mock_location"},
		)
		if !result.Success {
			return result
		}
		readback := m.runAndroid(ctx,
			commandVariant{Name: "cmd", Args: []string{"appops", "get", pkg, "android:mock_location"}, Strategy: "cmd_appops_mock_location"},
			commandVariant{Name: "appops", Args: []string{"get", pkg, "android:mock_location"}, Strategy: "appops_mock_location"},
		)
		if !readback.Success || !strings.Contains(strings.ToLower(readback.Stdout), mode) {
			return fail(
				"VERIFY_FAILED",
				"模拟位置 AppOp 修改后读回不一致",
				errors.New(readback.Stdout+" "+readback.Stderr),
				"appops_mock_location_readback",
			)
		}
		result.Message = "模拟位置应用权限修改并读回验证成功"
		result.Data = map[string]any{"package": pkg, "enabled": enabled, "mode": mode}
		result.Strategy += "_readback"
		return result
	case "set":
		name, parseErr := argString(req.Args, "name", "key")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		key, exists := keys[name]
		if !exists {
			return invalid("name 必须是 developer_options、usb_debugging 或 stay_awake")
		}
		value, parseErr := argString(req.Args, "value")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		return settingsPutVerified(ctx, m, "global", key, value, "current")
	default:
		return invalidAction(req.Tool, action, "adb", "animation", "get", "mock_location", "set", "stay_awake")
	}
}

func requiredEnabled(args map[string]any) (bool, error) {
	if _, exists := args["enabled"]; !exists {
		return false, errors.New("缺少布尔参数 enabled")
	}
	return argBool(args, "enabled", false)
}

func initServiceCommand(name, operation string) []string {
	return []string{"ctl." + operation, name}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
