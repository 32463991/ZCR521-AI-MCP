//go:build windows

package ops

import (
	"errors"
	"os/exec"
)

func configureProcess(cmd *exec.Cmd, spec commandSpec) error {
	if spec.UseCredential {
		return errors.New("Windows 主机不支持 Android UID 执行身份")
	}
	return nil
}

func terminateProcessTree(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func killProcessTree(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func platformEUID() int { return -1 }

func sendSignal(pid int, signal int) error {
	return errors.New("Windows 主机不支持 Unix signal")
}

func changePriority(pid, priority int) error {
	return errors.New("Windows 主机不支持 Unix setpriority")
}

func parseProcessUID(text string) (int, error) {
	return -1, errors.New("Windows 主机没有 Android UID")
}
