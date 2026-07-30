//go:build !windows

package main

import "os"

func effectiveUID() int {
	return os.Geteuid()
}
