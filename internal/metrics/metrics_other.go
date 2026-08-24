//go:build !linux && !windows

package metrics

import (
	"fmt"
	"github.com/jjp-monitor/jjp/internal/protocol"
)

func Collect() (protocol.Metrics, error) {
	return protocol.Metrics{}, fmt.Errorf("metrics collection is not implemented for this OS")
}
