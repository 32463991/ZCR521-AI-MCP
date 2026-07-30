//go:build !windows

package ops

import (
	"os"
	"syscall"
)

func platformOwnership(info os.FileInfo) (int, int) {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(stat.Uid), int(stat.Gid)
	}
	return -1, -1
}

func platformDiskUsage(path string) (map[string]any, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return nil, err
	}
	blockSize := uint64(stat.Bsize)
	total := stat.Blocks * blockSize
	free := stat.Bfree * blockSize
	available := stat.Bavail * blockSize
	return map[string]any{
		"path":           path,
		"totalBytes":     total,
		"freeBytes":      free,
		"availableBytes": available,
		"usedBytes":      total - free,
	}, nil
}
