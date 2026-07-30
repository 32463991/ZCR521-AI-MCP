//go:build !windows

package broker

import "os"

func chownSocket(path string, gid int) error {
	return os.Chown(path, 0, gid)
}
