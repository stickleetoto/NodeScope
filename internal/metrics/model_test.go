package metrics

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stickleetoto/NodeScope/internal/protocol"
)

type testCollector struct {
	name    string
	samples []Sample
	err     error
}

func (c testCollector) Name() string { return c.name }
func (c testCollector) Collect(context.Context) ([]Sample, error) {
	return append([]Sample(nil), c.samples...), c.err
}

func TestSampleValidateRejectsUnboundedAttributes(t *testing.T) {
	attrs := map[string]string{}
	for i := 0; i < maxAttributes+1; i++ {
		attrs[string(rune('a'+i))] = "x"
	}
	s := Sample{Name: "system.cpu.utilization", Value: 0.5, Kind: Gauge, Attributes: attrs}
	if err := s.Validate(); err == nil {
		t.Fatal("expected attribute limit error")
	}
}

func TestRegistryRejectsDuplicateCollectorNames(t *testing.T) {
	r, err := NewRegistry(testCollector{name: "host"})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Register(testCollector{name: "host"}); err == nil {
		t.Fatal("expected duplicate collector error")
	}
}

func TestRegistryKeepsPartialSamplesAndReportsCollectorError(t *testing.T) {
	r, err := NewRegistry(testCollector{
		name: "partial",
		samples: []Sample{{Name: "system.load", Value: 1.5, Unit: "1", Kind: Gauge}},
		err: errors.New("secondary source unavailable"),
	})
	if err != nil {
		t.Fatal(err)
	}
	samples, errs := r.Collect(context.Background())
	if len(samples) != 1 {
		t.Fatalf("samples=%d want 1", len(samples))
	}
	if len(errs) != 1 {
		t.Fatalf("errors=%d want 1", len(errs))
	}
	if samples[0].Collector != "partial" {
		t.Fatalf("collector=%q want partial", samples[0].Collector)
	}
	if samples[0].Timestamp.IsZero() {
		t.Fatal("registry should stamp zero timestamps")
	}
}

func TestLegacyToSamplesPreservesV1Meaning(t *testing.T) {
	temp := 42.5
	ts := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	got := legacyToSamples(protocol.Metrics{
		CPUPercent: 50,
		RAMPercent: 25,
		RAMUsedBytes: 256,
		RAMTotalBytes: 1024,
		DiskPercent: 75,
		DiskUsedBytes: 750,
		DiskTotalBytes: 1000,
		UptimeSeconds: 123,
		TemperatureC: &temp,
	}, ts)
	if len(got) != 9 {
		t.Fatalf("samples=%d want 9", len(got))
	}
	if got[0].Name != "system.cpu.utilization" || got[0].Value != 0.5 {
		t.Fatalf("unexpected CPU sample: %+v", got[0])
	}
	if got[len(got)-1].Name != "hw.temperature" || got[len(got)-1].Value != temp {
		t.Fatalf("unexpected temperature sample: %+v", got[len(got)-1])
	}
}
