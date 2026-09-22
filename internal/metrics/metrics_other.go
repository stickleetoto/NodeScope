//go:build !linux && !windows

package metrics

import (
	"fmt"
	"github.com/stickleetoto/NodeScope/internal/protocol"
)

func collectLegacy() (protocol.Metrics, error) {
	return protocol.Metrics{}, fmt.Errorf("metrics collection is not implemented for this OS")
}
