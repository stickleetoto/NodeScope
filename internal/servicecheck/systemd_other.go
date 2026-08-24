//go:build !linux

package servicecheck

import (
	"context"
	"fmt"
)

func checkSystemd(context.Context, string) error {
	return fmt.Errorf("systemd checks require Linux")
}
