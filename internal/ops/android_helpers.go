package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func isAndroidHost() bool {
	if runtime.GOOS == "android" {
		return true
	}
	_, buildErr := os.Stat("/system/build.prop")
	_, shellErr := os.Stat("/system/bin/sh")
	return buildErr == nil && shellErr == nil
}

func requireAndroid() *Result {
	if isAndroidHost() {
		return nil
	}
	result := unsupported("当前主机不是 Android；未执行 Android 系统命令")
	return &result
}

func requireAndroidRoot() *Result {
	if result := requireAndroid(); result != nil {
		return result
	}
	if platformEUID() != 0 {
		result := fail("ROOT_UNAVAILABLE", "当前服务未以 uid=0 运行", fmt.Errorf("effective uid=%d", platformEUID()), "identity_check")
		return &result
	}
	return nil
}

func androidUser(args map[string]any) (string, error) {
	value, ok := argAny(args, "user", "userId")
	if !ok {
		return "current", nil
	}
	switch typed := value.(type) {
	case string:
		if typed == "current" || typed == "all" {
			return typed, nil
		}
		id, err := strconv.Atoi(typed)
		if err != nil || id < 0 {
			return "", errors.New("user/userId 必须是 current、all 或非负整数")
		}
		return typed, nil
	default:
		clone := map[string]any{"user": typed}
		id, err := argInt64(clone, -1, "user")
		if err != nil || id < 0 {
			return "", errors.New("user/userId 必须是 current、all 或非负整数")
		}
		return strconv.FormatInt(id, 10), nil
	}
}

func packageName(args map[string]any) (string, error) {
	value, err := argString(args, "package", "packageName")
	if err != nil {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" || value == "." || value == ".." ||
		strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") ||
		strings.Contains(value, "..") {
		return "", errors.New("package 必须是合法包名")
	}
	for _, character := range value {
		if character != '.' && character != '_' &&
			(character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') {
			return "", errors.New("package 必须是合法包名")
		}
	}
	return value, nil
}

func componentName(args map[string]any) (string, error) {
	value, err := argString(args, "component")
	if err != nil {
		return "", err
	}
	if !strings.Contains(value, "/") || strings.ContainsAny(value, " \t\r\n") {
		return "", errors.New("component 必须形如 package/.Class")
	}
	return value, nil
}

func (m *Manager) runAndroid(ctx context.Context, variants ...commandVariant) Result {
	if result := requireAndroid(); result != nil {
		return *result
	}
	return runFirstAvailable(ctx, m, variants, m.cfg.ShellTimeout)
}

func (m *Manager) runAndroidRoot(ctx context.Context, variants ...commandVariant) Result {
	if result := requireAndroidRoot(); result != nil {
		return *result
	}
	return runFirstAvailable(ctx, m, variants, m.cfg.ShellTimeout)
}

func withData(result Result, data any) Result {
	result.Data = data
	return result
}

func commandSucceededWith(result Result, token string) bool {
	return result.Success && (token == "" || strings.Contains(strings.ToLower(result.Stdout), strings.ToLower(token)))
}

func getProp(ctx context.Context, m *Manager, key string) Result {
	return m.runAndroid(ctx,
		commandVariant{Name: "getprop", Args: []string{key}, Strategy: "getprop"},
	)
}

func settingsGet(ctx context.Context, m *Manager, namespace, key, user string) Result {
	args := []string{}
	if user != "" && user != "current" {
		args = append(args, "--user", user)
	}
	args = append(args, "get", namespace, key)
	return m.runAndroid(ctx,
		commandVariant{Name: "settings", Args: args, Strategy: "settings_cli"},
		commandVariant{Name: "cmd", Args: append([]string{"settings"}, args...), Strategy: "cmd_settings"},
	)
}

func settingsPutVerified(ctx context.Context, m *Manager, namespace, key, value, user string) Result {
	args := []string{}
	if user != "" && user != "current" {
		args = append(args, "--user", user)
	}
	args = append(args, "put", namespace, key, value)
	result := m.runAndroidRoot(ctx,
		commandVariant{Name: "settings", Args: args, Strategy: "settings_cli"},
		commandVariant{Name: "cmd", Args: append([]string{"settings"}, args...), Strategy: "cmd_settings"},
	)
	if !result.Success {
		return result
	}
	readback := settingsGet(ctx, m, namespace, key, user)
	if !readback.Success || strings.TrimSpace(readback.Stdout) != value {
		verify := fail("VERIFY_FAILED", "设置命令执行后读回值不一致", fmt.Errorf("expected %q, got %q", value, strings.TrimSpace(readback.Stdout)), "settings_readback")
		verify.Stdout = result.Stdout
		verify.Stderr = result.Stderr + readback.Stderr
		verify.ExitCode = readback.ExitCode
		return verify
	}
	result.Message = "系统设置修改并读回验证成功"
	result.Data = map[string]string{"namespace": namespace, "key": key, "value": value, "user": user}
	result.Strategy += "_readback"
	return result
}

func settingsDeleteVerified(ctx context.Context, m *Manager, namespace, key, user string) Result {
	args := []string{}
	if user != "" && user != "current" {
		args = append(args, "--user", user)
	}
	args = append(args, "delete", namespace, key)
	result := m.runAndroidRoot(ctx,
		commandVariant{Name: "settings", Args: args, Strategy: "settings_cli"},
		commandVariant{Name: "cmd", Args: append([]string{"settings"}, args...), Strategy: "cmd_settings"},
	)
	if !result.Success {
		return result
	}
	readback := settingsGet(ctx, m, namespace, key, user)
	actual := strings.TrimSpace(readback.Stdout)
	if !readback.Success || actual != "null" && actual != "" {
		return fail("VERIFY_FAILED", "设置删除后读回仍存在", fmt.Errorf("got %q", actual), "settings_readback")
	}
	result.Message = "系统设置删除并读回验证成功"
	result.Data = map[string]string{"namespace": namespace, "key": key, "user": user}
	result.Strategy += "_readback"
	return result
}

func validateSettingsNamespace(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "global", "secure", "system":
		return value, nil
	default:
		return "", errors.New("namespace 必须是 global、secure 或 system")
	}
}

func parseKeyValueLines(text string) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(text, "\n") {
		if index := strings.IndexByte(line, '='); index > 0 {
			out[strings.TrimSpace(line[:index])] = strings.TrimSpace(line[index+1:])
		}
	}
	return out
}

func androidAPILevel(ctx context.Context, m *Manager) int {
	result := getProp(ctx, m, "ro.build.version.sdk")
	if !result.Success {
		return 0
	}
	value, _ := strconv.Atoi(strings.TrimSpace(result.Stdout))
	return value
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func resultFromReadback(operation, actual string, command Result) Result {
	if !command.Success {
		return command
	}
	command.Message = operation + "成功并完成读回"
	command.Data = map[string]string{"value": actual}
	command.Strategy += "_readback"
	return command
}

func shortAndroidTimeout() time.Duration {
	return 30 * time.Second
}
