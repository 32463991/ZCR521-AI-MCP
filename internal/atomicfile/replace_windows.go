//go:build windows

package atomicfile

import (
	"syscall"
	"unsafe"
)

var (
	kernel32MoveFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")
)

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8
)

func replace(source, destination string) error {
	src, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	dst, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	r, _, callErr := kernel32MoveFileEx.Call(
		uintptr(unsafe.Pointer(src)),
		uintptr(unsafe.Pointer(dst)),
		moveFileReplaceExisting|moveFileWriteThrough,
	)
	if r == 0 {
		return callErr
	}
	return nil
}
