//go:build windows

package ops

import (
	"errors"
	"os"
)

func platformOwnership(info os.FileInfo) (int, int) {
	return -1, -1
}

func platformDiskUsage(path string) (map[string]any, error) {
	return nil, errors.New("Windows 测试主机不提供 Android statfs")
}
