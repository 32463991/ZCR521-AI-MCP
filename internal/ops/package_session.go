package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var packageSessionPattern = regexp.MustCompile(`(?i)(?:session\s*\[|session(?:Id)?\s*[=:]\s*)(\d+)`)

func (m *Manager) installAPKSession(ctx context.Context, paths []string, user string, replace, downgrade, grant bool) Result {
	createArgs := []string{"install-create"}
	if replace {
		createArgs = append(createArgs, "-r")
	}
	if downgrade {
		createArgs = append(createArgs, "-d")
	}
	if grant {
		createArgs = append(createArgs, "-g")
	}
	if user != "" && user != "current" {
		createArgs = append(createArgs, "--user", user)
	}
	create := m.runAndroidRoot(ctx,
		commandVariant{Name: "pm", Args: createArgs, Strategy: "pm_install_session_create"},
		commandVariant{Name: "cmd", Args: append([]string{"package"}, createArgs...), Strategy: "cmd_package_session_create"},
	)
	if !create.Success {
		return create
	}
	sessionID, err := parsePackageSessionID(create.Stdout + "\n" + create.Stderr)
	if err != nil {
		return fail("INSTALL_FAILED", "Package Manager 创建会话但未返回 session ID", err, "pm_install_session_create")
	}
	committed := false
	defer func() {
		if !committed {
			_ = m.abandonPackageSession(context.Background(), sessionID)
		}
	}()
	var stdout strings.Builder
	var stderr strings.Builder
	stdout.WriteString(create.Stdout)
	stderr.WriteString(create.Stderr)
	for index, path := range paths {
		info, statErr := os.Stat(path)
		if statErr != nil {
			return fileFailure("安装会话 APK 读取失败", statErr, "pm_install_session_write")
		}
		name := sanitizeSessionSplitName(filepath.Base(path), index)
		writeArgs := []string{"install-write", "-S", strconv.FormatInt(info.Size(), 10), strconv.Itoa(sessionID), name, path}
		write := m.runAndroidRoot(ctx,
			commandVariant{Name: "pm", Args: writeArgs, Strategy: "pm_install_session_write"},
			commandVariant{Name: "cmd", Args: append([]string{"package"}, writeArgs...), Strategy: "cmd_package_session_write"},
		)
		stdout.WriteString(write.Stdout)
		stderr.WriteString(write.Stderr)
		if !write.Success || !strings.Contains(strings.ToLower(write.Stdout+write.Stderr), "success") {
			if write.Success {
				write.Success = false
				write.Code = "INSTALL_FAILED"
				write.Message = "Package Manager install-write 未返回 Success"
				write.Error = strings.TrimSpace(write.Stdout + " " + write.Stderr)
			}
			write.Stdout = stdout.String()
			write.Stderr = stderr.String()
			write.Data = map[string]any{"sessionId": sessionID, "failedSplit": name}
			return write
		}
	}
	commitArgs := []string{"install-commit", strconv.Itoa(sessionID)}
	commit := m.runAndroidRoot(ctx,
		commandVariant{Name: "pm", Args: commitArgs, Strategy: "pm_install_session_commit"},
		commandVariant{Name: "cmd", Args: append([]string{"package"}, commitArgs...), Strategy: "cmd_package_session_commit"},
	)
	stdout.WriteString(commit.Stdout)
	stderr.WriteString(commit.Stderr)
	commit.Stdout = stdout.String()
	commit.Stderr = stderr.String()
	commit.Data = map[string]any{"sessionId": sessionID, "apkCount": len(paths)}
	if !commit.Success || !strings.Contains(strings.ToLower(commit.Stdout+commit.Stderr), "success") {
		if commit.Success {
			commit.Success = false
			commit.Code = "INSTALL_FAILED"
			commit.Message = "Package Manager install-commit 未返回 Success"
			commit.Error = strings.TrimSpace(commit.Stdout + " " + commit.Stderr)
		}
		return commit
	}
	committed = true
	commit.Message = "Package Manager Session 写入并提交成功"
	commit.Strategy = "pm_install_create_write_commit"
	return commit
}

func (m *Manager) abandonPackageSession(ctx context.Context, sessionID int) error {
	args := []string{"install-abandon", strconv.Itoa(sessionID)}
	result := m.runAndroidRoot(ctx,
		commandVariant{Name: "pm", Args: args, Strategy: "pm_install_session_abandon"},
		commandVariant{Name: "cmd", Args: append([]string{"package"}, args...), Strategy: "cmd_package_session_abandon"},
	)
	if !result.Success {
		return errors.New(result.Error)
	}
	return nil
}

func parsePackageSessionID(output string) (int, error) {
	match := packageSessionPattern.FindStringSubmatch(output)
	if len(match) != 2 {
		return 0, fmt.Errorf("无法解析 session ID：%s", strings.TrimSpace(output))
	}
	sessionID, err := strconv.Atoi(match[1])
	if err != nil || sessionID < 0 {
		return 0, errors.New("session ID 无效")
	}
	return sessionID, nil
}

func sanitizeSessionSplitName(name string, index int) string {
	name = strings.TrimSuffix(name, filepath.Ext(name))
	var builder strings.Builder
	for _, character := range name {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' {
			builder.WriteRune(character)
		}
	}
	if builder.Len() == 0 {
		return fmt.Sprintf("split_%d", index)
	}
	return fmt.Sprintf("%02d_%s", index, builder.String())
}

func (m *Manager) appInstallSessionAction(ctx context.Context, req Request, user string) Result {
	operation, err := argString(req.Args, "operation")
	if err != nil {
		return invalid("session action 需要 operation=create/write/commit/abandon")
	}
	operation = normalizeTool(operation)
	switch operation {
	case "create":
		args := []string{"install-create"}
		if user != "current" {
			args = append(args, "--user", user)
		}
		result := m.runAndroidRoot(ctx,
			commandVariant{Name: "pm", Args: args, Strategy: "pm_install_session_create"},
			commandVariant{Name: "cmd", Args: append([]string{"package"}, args...), Strategy: "cmd_package_session_create"},
		)
		if result.Success {
			sessionID, parseErr := parsePackageSessionID(result.Stdout + result.Stderr)
			if parseErr != nil {
				return fail("INSTALL_FAILED", "创建安装会话后无法解析 ID", parseErr, result.Strategy)
			}
			result.Data = map[string]int{"sessionId": sessionID}
		}
		return result
	case "write":
		sessionID, parseErr := argInt64(req.Args, -1, "sessionId")
		if parseErr != nil || sessionID < 0 {
			return invalid("sessionId 必须是非负整数")
		}
		pathValue, pathErr := argString(req.Args, "path", "apk")
		if pathErr != nil {
			return invalid(pathErr.Error())
		}
		path, pathErr := m.resolvePath(pathValue)
		if pathErr != nil {
			return invalid(pathErr.Error())
		}
		info, statErr := os.Stat(path)
		if statErr != nil {
			return fileFailure("APK 不存在", statErr, "pm_install_session_write")
		}
		name, _ := argOptionalString(req.Args, sanitizeSessionSplitName(filepath.Base(path), 0), "name", "splitName")
		args := []string{"install-write", "-S", strconv.FormatInt(info.Size(), 10), strconv.FormatInt(sessionID, 10), name, path}
		return m.runAndroidRoot(ctx,
			commandVariant{Name: "pm", Args: args, Strategy: "pm_install_session_write"},
			commandVariant{Name: "cmd", Args: append([]string{"package"}, args...), Strategy: "cmd_package_session_write"},
		)
	case "commit", "abandon":
		sessionID, parseErr := argInt64(req.Args, -1, "sessionId")
		if parseErr != nil || sessionID < 0 {
			return invalid("sessionId 必须是非负整数")
		}
		subcommand := "install-" + operation
		args := []string{subcommand, strconv.FormatInt(sessionID, 10)}
		return m.runAndroidRoot(ctx,
			commandVariant{Name: "pm", Args: args, Strategy: "pm_install_session_" + operation},
			commandVariant{Name: "cmd", Args: append([]string{"package"}, args...), Strategy: "cmd_package_session_" + operation},
		)
	default:
		return invalid("session.operation 必须是 create、write、commit 或 abandon")
	}
}
