//go:build linux

package metrics

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"
)

type processCollector struct{}

func (processCollector) Name() string { return "process" }

func (processCollector) Collect(ctx context.Context) ([]Sample, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	counts := map[string]int{"total": 0}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		counts["total"]++
		b, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		line := string(b)
		idx := strings.LastIndex(line, ") ")
		if idx < 0 || idx+2 >= len(line) {
			continue
		}
		state := line[idx+2 : idx+3]
		switch state {
		case "R":
			counts["running"]++
		case "S", "I":
			counts["sleeping"]++
		case "D":
			counts["blocked"]++
		case "Z":
			counts["zombie"]++
		case "T", "t":
			counts["stopped"]++
		default:
			counts["other"]++
		}
	}
	ts := time.Now().UTC()
	order := []string{"total", "running", "sleeping", "blocked", "zombie", "stopped", "other"}
	out := make([]Sample, 0, len(order))
	for _, state := range order {
		value, ok := counts[state]
		if !ok && state != "total" {
			continue
		}
		out = append(out, Sample{
			Name: "system.process.count", Value: float64(value), Unit: "{process}", Kind: Gauge, Timestamp: ts,
			Attributes: map[string]string{"state": state},
		})
	}
	return out, nil
}
