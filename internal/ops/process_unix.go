//go:build !windows

package ops

import (
	"fmt"
	"os/exec"
	"strconv"
	"syscall"
)

func configureProcess(cmd *exec.Cmd, spec commandSpec) error {
	attrs := &syscall.SysProcAttr{Setpgid: true}
	if spec.UseCredential {
		if spec.UID < 0 || spec.GID < 0 {
			return fmt.Errorf("uid/gid 不能为负数")
		}
		attrs.Credential = &syscall.Credential{Uid: uint32(spec.UID), Gid: uint32(spec.GID)}
	}
	cmd.SysProcAttr = attrs
	return nil
}

func terminateProcessTree(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	}
}

func killProcessTree(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

func platformEUID() int {
	return syscall.Geteuid()
}

func sendSignal(pid int, signal int) error {
	return syscall.Kill(pid, syscall.Signal(signal))
}

func changePriority(pid, priority int) error {
	return syscall.Setpriority(syscall.PRIO_PROCESS, pid, priority)
}

func parseProcessUID(text string) (int, error) {
	value, err := strconv.Atoi(text)
	if err != nil {
		return -1, err
	}
	return value, nil
}
