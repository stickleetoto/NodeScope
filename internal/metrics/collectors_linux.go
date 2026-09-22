//go:build linux

package metrics

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const maxNetworkInterfaces = 32

type loadCollector struct{}

func (loadCollector) Name() string { return "load" }

func (loadCollector) Collect(ctx context.Context) ([]Sample, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(b))
	if len(fields) < 3 {
		return nil, fmt.Errorf("unexpected /proc/loadavg")
	}
	ts := time.Now().UTC()
	windows := []string{"1m", "5m", "15m"}
	out := make([]Sample, 0, 3)
	for i := 0; i < 3; i++ {
		v, err := strconv.ParseFloat(fields[i], 64)
		if err != nil {
			return nil, fmt.Errorf("parse load %s: %w", windows[i], err)
		}
		out = append(out, Sample{
			Name: "system.load.average", Value: v, Unit: "1", Kind: Gauge, Timestamp: ts,
			Attributes: map[string]string{"window": windows[i]},
		})
	}
	return out, nil
}

type swapCollector struct{}

func (swapCollector) Name() string { return "swap" }

func (swapCollector) Collect(ctx context.Context) ([]Sample, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var total, free uint64
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		v, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			continue
		}
		switch strings.TrimSuffix(fields[0], ":") {
		case "SwapTotal":
			total = v * 1024
		case "SwapFree":
			free = v * 1024
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if total == 0 {
		return []Sample{
			{Name: "system.swap.usage", Value: 0, Unit: "By", Kind: Gauge},
			{Name: "system.swap.limit", Value: 0, Unit: "By", Kind: Gauge},
			{Name: "system.swap.utilization", Value: 0, Unit: "1", Kind: Gauge},
		}, nil
	}
	used := total - free
	return []Sample{
		{Name: "system.swap.usage", Value: float64(used), Unit: "By", Kind: Gauge},
		{Name: "system.swap.limit", Value: float64(total), Unit: "By", Kind: Gauge},
		{Name: "system.swap.utilization", Value: float64(used) / float64(total), Unit: "1", Kind: Gauge},
	}, nil
}

type networkCollector struct{}

func (networkCollector) Name() string { return "network" }

func (networkCollector) Collect(ctx context.Context) ([]Sample, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := os.ReadFile("/proc/net/dev")
	if err != nil {
		return nil, err
	}
	return parseNetDev(b, time.Now().UTC())
}

func parseNetDev(b []byte, ts time.Time) ([]Sample, error) {
	lines := strings.Split(string(b), "\n")
	out := make([]Sample, 0)
	interfaces := 0
	truncated := false
	for _, line := range lines {
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		iface := strings.TrimSpace(line[:colon])
		fields := strings.Fields(line[colon+1:])
		if iface == "" || len(fields) < 16 {
			continue
		}
		if interfaces >= maxNetworkInterfaces {
			truncated = true
			continue
		}
		values := make([]uint64, 16)
		valid := true
		for i := 0; i < 16; i++ {
			v, err := strconv.ParseUint(fields[i], 10, 64)
			if err != nil {
				valid = false
				break
			}
			values[i] = v
		}
		if !valid {
			continue
		}
		interfaces++
		appendDirection := func(direction string, bytes, packets, errs, drops uint64) {
			attrs := map[string]string{"interface": iface, "direction": direction}
			out = append(out,
				Sample{Name: "system.network.io", Value: float64(bytes), Unit: "By", Kind: Counter, Timestamp: ts, Attributes: attrs},
				Sample{Name: "system.network.packets", Value: float64(packets), Unit: "{packet}", Kind: Counter, Timestamp: ts, Attributes: attrs},
				Sample{Name: "system.network.errors", Value: float64(errs), Unit: "{error}", Kind: Counter, Timestamp: ts, Attributes: attrs},
				Sample{Name: "system.network.drops", Value: float64(drops), Unit: "{packet}", Kind: Counter, Timestamp: ts, Attributes: attrs},
			)
		}
		appendDirection("receive", values[0], values[1], values[2], values[3])
		appendDirection("transmit", values[8], values[9], values[10], values[11])
	}
	if truncated {
		return out, fmt.Errorf("network interface limit %d reached", maxNetworkInterfaces)
	}
	return out, nil
}

type psiCollector struct{}

func (psiCollector) Name() string { return "psi" }

func (psiCollector) Collect(ctx context.Context) ([]Sample, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ts := time.Now().UTC()
	resources := []string{"cpu", "memory", "io"}
	var out []Sample
	var errs []string
	for _, resource := range resources {
		b, err := os.ReadFile("/proc/pressure/" + resource)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			errs = append(errs, resource+": "+err.Error())
			continue
		}
		samples, err := parsePSI(resource, b, ts)
		if err != nil {
			errs = append(errs, resource+": "+err.Error())
			continue
		}
		out = append(out, samples...)
	}
	if len(errs) > 0 {
		return out, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return out, nil
}

func parsePSI(resource string, b []byte, ts time.Time) ([]Sample, error) {
	var out []Sample
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 2 {
			continue
		}
		scope := fields[0]
		if scope != "some" && scope != "full" {
			continue
		}
		for _, field := range fields[1:] {
			k, v, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			switch k {
			case "avg10", "avg60", "avg300":
				n, err := strconv.ParseFloat(v, 64)
				if err != nil {
					return nil, fmt.Errorf("parse %s %s %s: %w", resource, scope, k, err)
				}
				out = append(out, Sample{
					Name: "system.pressure.stall", Value: n / 100, Unit: "1", Kind: Gauge, Timestamp: ts,
					Attributes: map[string]string{"resource": resource, "scope": scope, "window": strings.TrimPrefix(k, "avg") + "s"},
				})
			case "total":
				n, err := strconv.ParseUint(v, 10, 64)
				if err != nil {
					return nil, fmt.Errorf("parse %s %s total: %w", resource, scope, err)
				}
				out = append(out, Sample{
					Name: "system.pressure.stall.time", Value: float64(n), Unit: "us", Kind: Counter, Timestamp: ts,
					Attributes: map[string]string{"resource": resource, "scope": scope},
				})
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func platformCollectors() []Collector {
	return []Collector{loadCollector{}, swapCollector{}, networkCollector{}, psiCollector{}}
}
