//go:build linux

package servicecheck

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

func checkSystemd(ctx context.Context, unit string) error {
	cmd := exec.CommandContext(ctx, "systemctl", "is-active", unit)
	out, err := cmd.CombinedOutput()
	state := strings.TrimSpace(string(out))
	if err != nil {
		if state == "" {
			state = err.Error()
		}
		return fmt.Errorf("%s", state)
	}
	if state != "active" {
		return fmt.Errorf("%s", state)
	}
	return nil
}
