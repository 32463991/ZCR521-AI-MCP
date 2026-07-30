//go:build windows

package supervisor

import (
	"errors"
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func configureChild(command *exec.Cmd, _, _ int, _ bool) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func signalProcessGroup(process *os.Process, signal syscall.Signal) error {
	if signal == syscall.SIGTERM || signal == syscall.SIGKILL {
		return process.Kill()
	}
	return process.Signal(os.Interrupt)
}

func processAlive(process *os.Process) bool {
	if process == nil || process.Pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		uint32(process.Pid),
	)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	if windows.GetExitCodeProcess(handle, &exitCode) != nil {
		return false
	}
	const stillActive = 259
	return exitCode == stillActive
}

func replaceCurrentProcess(string, []string) error {
	return errors.New("Windows 主机测试不支持进程内回滚 exec")
}
