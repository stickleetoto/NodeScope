//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd

package filelock

import (
	"fmt"
	"os"
)

type Lock struct{ path string }

func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("state is already locked by another jjp process")
		}
		return nil, err
	}
	_ = f.Close()
	return &Lock{path: path}, nil
}

func (l *Lock) Release() error {
	if l == nil || l.path == "" {
		return nil
	}
	err := os.Remove(l.path)
	l.path = ""
	return err
}
