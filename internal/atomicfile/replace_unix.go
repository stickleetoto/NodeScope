//go:build !windows

package atomicfile

import (
	"os"
)

func replace(src, dst string) error { return os.Rename(src, dst) }

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
