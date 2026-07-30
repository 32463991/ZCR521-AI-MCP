//go:build windows

package service

func effectiveUID() int {
	return -1
}
