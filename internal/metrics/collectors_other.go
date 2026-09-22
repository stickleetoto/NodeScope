//go:build !linux && !windows

package metrics

func platformCollectors() []Collector { return nil }
