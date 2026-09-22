//go:build !linux

package metrics

func platformCollectors() []Collector { return nil }
