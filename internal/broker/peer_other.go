//go:build !linux && !android

package broker

import (
	"errors"
	"net"
)

func inspectPeer(*net.UnixConn) (Peer, error) {
	return Peer{Verified: false, Strategy: "platform_without_SO_PEERCRED"}, errors.New("SO_PEERCRED is unavailable on this host")
}
