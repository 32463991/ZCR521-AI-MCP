//go:build linux || android

package broker

import (
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

func inspectPeer(connection *net.UnixConn) (Peer, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return Peer{}, err
	}
	var (
		credential *unix.Ucred
		socketErr  error
	)
	if err := raw.Control(func(fd uintptr) {
		credential, socketErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return Peer{}, err
	}
	if socketErr != nil {
		return Peer{}, fmt.Errorf("SO_PEERCRED: %w", socketErr)
	}
	return Peer{
		PID:      int(credential.Pid),
		UID:      credential.Uid,
		GID:      credential.Gid,
		Verified: true,
		Strategy: "SO_PEERCRED+frontend_pid",
	}, nil
}
