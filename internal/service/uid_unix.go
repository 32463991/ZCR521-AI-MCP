//go:build !windows

package service

import "os"

func effectiveUID() int {
	return os.Geteuid()
}
