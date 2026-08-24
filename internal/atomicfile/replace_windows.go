//go:build windows

package atomicfile

import (
	"os"
	"syscall"
	"unsafe"
)

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8
)

var (
	kernel32       = syscall.NewLazyDLL("kernel32.dll")
	moveFileExProc = kernel32.NewProc("MoveFileExW")
)

func replace(src, dst string) error {
	srcp, err := syscall.UTF16PtrFromString(src)
	if err != nil {
		return err
	}
	dstp, err := syscall.UTF16PtrFromString(dst)
	if err != nil {
		return err
	}
	r1, _, e1 := moveFileExProc.Call(
		uintptr(unsafe.Pointer(srcp)),
		uintptr(unsafe.Pointer(dstp)),
		uintptr(moveFileReplaceExisting|moveFileWriteThrough),
	)
	if r1 == 0 {
		if e1 != syscall.Errno(0) {
			return e1
		}
		return os.ErrInvalid
	}
	return nil
}

// MoveFileEx with MOVEFILE_WRITE_THROUGH flushes the rename metadata.
func syncDir(string) error { return nil }
