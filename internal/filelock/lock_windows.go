//go:build windows

package filelock

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	lockfileFailImmediately = 0x00000001
	lockfileExclusiveLock   = 0x00000002
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	lockFileExProc   = kernel32.NewProc("LockFileEx")
	unlockFileExProc = kernel32.NewProc("UnlockFileEx")
)

type Lock struct {
	f  *os.File
	ov syscall.Overlapped
}

func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	l := &Lock{f: f}
	r1, _, e1 := lockFileExProc.Call(
		f.Fd(),
		uintptr(lockfileExclusiveLock|lockfileFailImmediately),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&l.ov)),
	)
	if r1 == 0 {
		_ = f.Close()
		if e1 != syscall.Errno(0) {
			return nil, fmt.Errorf("state is already locked by another jjp process: %w", e1)
		}
		return nil, fmt.Errorf("state is already locked by another jjp process")
	}
	return l, nil
}

func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	_, _, _ = unlockFileExProc.Call(l.f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&l.ov)))
	err := l.f.Close()
	l.f = nil
	return err
}
