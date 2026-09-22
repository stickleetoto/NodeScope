package metrics

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jjp-monitor/jjp/internal/protocol"
)

type MetricKind string

const (
	Gauge   MetricKind = "gauge"
	Counter MetricKind = "counter"

	maxMetricNameLen = 128
	maxCollectorName = 64
	maxAttributes    = 8
	maxAttrKeyLen    = 64
	maxAttrValueLen  = 128
)

type Sample struct {
	Name       string            `json:"name"`
	Value      float64           `json:"value"`
	Unit       string            `json:"unit,omitempty"`
	Kind       MetricKind        `json:"kind"`
	Timestamp  time.Time         `json:"timestamp"`
	Attributes map[string]string `json:"attributes,omitempty"`
	Collector  string            `json:"collector,omitempty"`
}

func (s Sample) Validate() error {
	if strings.TrimSpace(s.Name) == "" || len(s.Name) > maxMetricNameLen {
		return fmt.Errorf("metric name must be 1..%d bytes", maxMetricNameLen)
	}
	for _, r := range s.Name {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-') {
			return fmt.Errorf("metric name %q contains unsupported character %q", s.Name, r)
		}
	}
	if math.IsNaN(s.Value) || math.IsInf(s.Value, 0) {
		return fmt.Errorf("metric %q value must be finite", s.Name)
	}
	if s.Kind != Gauge && s.Kind != Counter {
		return fmt.Errorf("metric %q has unsupported kind %q", s.Name, s.Kind)
	}
	if len(s.Attributes) > maxAttributes {
		return fmt.Errorf("metric %q has more than %d attributes", s.Name, maxAttributes)
	}
	for k, v := range s.Attributes {
		if strings.TrimSpace(k) == "" || len(k) > maxAttrKeyLen {
			return fmt.Errorf("metric %q attribute key must be 1..%d bytes", s.Name, maxAttrKeyLen)
		}
		if len(v) > maxAttrValueLen {
			return fmt.Errorf("metric %q attribute %q exceeds %d bytes", s.Name, k, maxAttrValueLen)
		}
	}
	if len(s.Collector) > maxCollectorName {
		return fmt.Errorf("metric %q collector exceeds %d bytes", s.Name, maxCollectorName)
	}
	return nil
}

type Collector interface {
	Name() string
	Collect(context.Context) ([]Sample, error)
}

type CollectorError struct {
	Collector string
	Err       error
}

func (e CollectorError) Error() string {
	return fmt.Sprintf("%s collector: %v", e.Collector, e.Err)
}

type Registry struct {
	collectors []Collector
	names      map[string]struct{}
}

func NewRegistry(collectors ...Collector) (*Registry, error) {
	r := &Registry{names: map[string]struct{}{}}
	for _, c := range collectors {
		if err := r.Register(c); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (r *Registry) Register(c Collector) error {
	if c == nil {
		return fmt.Errorf("collector is nil")
	}
	name := strings.TrimSpace(c.Name())
	if name == "" || len(name) > maxCollectorName {
		return fmt.Errorf("collector name must be 1..%d bytes", maxCollectorName)
	}
	if r.names == nil {
		r.names = map[string]struct{}{}
	}
	if _, exists := r.names[name]; exists {
		return fmt.Errorf("collector %q already registered", name)
	}
	r.collectors = append(r.collectors, c)
	r.names[name] = struct{}{}
	return nil
}

func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.collectors))
	for _, c := range r.collectors {
		out = append(out, c.Name())
	}
	sort.Strings(out)
	return out
}

func (r *Registry) Collect(ctx context.Context) ([]Sample, []CollectorError) {
	now := time.Now().UTC()
	var out []Sample
	var errs []CollectorError
	for _, c := range r.collectors {
		if err := ctx.Err(); err != nil {
			errs = append(errs, CollectorError{Collector: c.Name(), Err: err})
			break
		}
		samples, err := c.Collect(ctx)
		if err != nil {
			errs = append(errs, CollectorError{Collector: c.Name(), Err: err})
		}
		for i := range samples {
			if samples[i].Timestamp.IsZero() {
				samples[i].Timestamp = now
			} else {
				samples[i].Timestamp = samples[i].Timestamp.UTC()
			}
			if samples[i].Collector == "" {
				samples[i].Collector = c.Name()
			}
			if vErr := samples[i].Validate(); vErr != nil {
				errs = append(errs, CollectorError{Collector: c.Name(), Err: vErr})
				continue
			}
			out = append(out, samples[i])
		}
	}
	return out, errs
}

type hostCollector struct{}

func (hostCollector) Name() string { return "host" }

func (hostCollector) Collect(ctx context.Context) ([]Sample, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m, err := collectLegacy()
	if err != nil {
		return nil, err
	}
	return legacyToSamples(m, time.Now().UTC()), nil
}

func DefaultRegistry() *Registry {
	r, err := NewRegistry(hostCollector{})
	if err != nil {
		panic(err)
	}
	return r
}

// Collect preserves the v1 protocol.Metrics collection path while Collector v2
// is introduced alongside it. Existing heartbeat payloads remain unchanged.
func Collect() (protocol.Metrics, error) {
	return collectLegacy()
}

func CollectSamples(ctx context.Context) ([]Sample, []CollectorError) {
	return DefaultRegistry().Collect(ctx)
}

func legacyToSamples(m protocol.Metrics, ts time.Time) []Sample {
	ts = ts.UTC()
	out := []Sample{
		{Name: "system.cpu.utilization", Value: m.CPUPercent / 100, Unit: "1", Kind: Gauge, Timestamp: ts},
		{Name: "system.memory.utilization", Value: m.RAMPercent / 100, Unit: "1", Kind: Gauge, Timestamp: ts},
		{Name: "system.memory.usage", Value: float64(m.RAMUsedBytes), Unit: "By", Kind: Gauge, Timestamp: ts},
		{Name: "system.memory.limit", Value: float64(m.RAMTotalBytes), Unit: "By", Kind: Gauge, Timestamp: ts},
		{Name: "system.filesystem.utilization", Value: m.DiskPercent / 100, Unit: "1", Kind: Gauge, Timestamp: ts, Attributes: map[string]string{"mount": "/"}},
		{Name: "system.filesystem.usage", Value: float64(m.DiskUsedBytes), Unit: "By", Kind: Gauge, Timestamp: ts, Attributes: map[string]string{"mount": "/"}},
		{Name: "system.filesystem.limit", Value: float64(m.DiskTotalBytes), Unit: "By", Kind: Gauge, Timestamp: ts, Attributes: map[string]string{"mount": "/"}},
		{Name: "system.uptime", Value: float64(m.UptimeSeconds), Unit: "s", Kind: Gauge, Timestamp: ts},
	}
	if m.TemperatureC != nil {
		out = append(out, Sample{Name: "hw.temperature", Value: *m.TemperatureC, Unit: "Cel", Kind: Gauge, Timestamp: ts, Attributes: map[string]string{"sensor": "host"}})
	}
	return out
}
