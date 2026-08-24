//go:build !linux

package autostart

import (
	"fmt"
	"time"
)

func Install(string, time.Duration, bool) (string, error) {
	return "", fmt.Errorf("automatic agent installation currently requires systemd on Linux")
}
