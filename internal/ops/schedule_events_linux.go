//go:build linux || android

package ops

import (
	"context"
	"errors"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

func startScheduleEventListener(ctx context.Context, kind string, events chan<- scheduleEvent) error {
	protocol := 0
	groups := uint32(0)
	switch kind {
	case "network":
		protocol = unix.NETLINK_ROUTE
		groups = unix.RTMGRP_LINK | unix.RTMGRP_IPV4_IFADDR | unix.RTMGRP_IPV6_IFADDR | unix.RTMGRP_IPV4_ROUTE | unix.RTMGRP_IPV6_ROUTE
	case "charging":
		protocol = unix.NETLINK_KOBJECT_UEVENT
		groups = 1
	default:
		return errors.New("未知事件监听类型")
	}
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW|unix.SOCK_CLOEXEC, protocol)
	if err != nil {
		return err
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: groups}); err != nil {
		_ = unix.Close(fd)
		return err
	}
	var closeOnce sync.Once
	closeFD := func() { closeOnce.Do(func() { _ = unix.Close(fd) }) }
	go func() {
		<-ctx.Done()
		closeFD()
	}()
	go func() {
		defer closeFD()
		buffer := make([]byte, 64*1024)
		for {
			count, _, receiveErr := unix.Recvfrom(fd, buffer, 0)
			if receiveErr != nil {
				return
			}
			trigger := false
			if kind == "network" {
				trigger = parseNetlinkRouteEvent(buffer[:count]) && hasActiveNetwork()
			} else {
				trigger = ueventIndicatesCharging(buffer[:count])
			}
			if trigger {
				select {
				case events <- scheduleEvent{Kind: kind, At: time.Now()}:
				default:
				}
			}
		}
	}()
	return nil
}
