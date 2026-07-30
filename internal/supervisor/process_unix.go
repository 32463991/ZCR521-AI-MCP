//go:build !windows

package supervisor

import (
	"os"
	"os/exec"
	"syscall"
)

func configureChild(command *exec.Cmd, uid, gid int, drop bool) {
	attributes := &syscall.SysProcAttr{Setsid: true}
	if drop {
		attributes.Credential = &syscall.Credential{
			Uid:    uint32(uid),
			Gid:    uint32(gid),
			Groups: []uint32{},
		}
	}
	command.SysProcAttr = attributes
}

func signalProcessGroup(process *os.Process, signal syscall.Signal) error {
	return syscall.Kill(-process.Pid, signal)
}

func processAlive(process *os.Process) bool {
	return process != nil && syscall.Kill(process.Pid, 0) == nil
}

func replaceCurrentProcess(binary string, args []string) error {
	return syscall.Exec(binary, append([]string{binary}, args[1:]...), os.Environ())
}
