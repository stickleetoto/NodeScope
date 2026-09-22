//go:build linux

package metrics

import (
	"testing"
	"time"
)

func TestParseNetDev(t *testing.T) {
	input := []byte(`Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
  eth0: 1000 10 2 3 0 0 0 0 2000 20 4 5 0 0 0 0
`)
	ts := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	samples, err := parseNetDev(input, ts)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 8 {
		t.Fatalf("samples=%d want 8", len(samples))
	}
	if samples[0].Name != "system.network.io" || samples[0].Value != 1000 {
		t.Fatalf("unexpected receive byte sample: %+v", samples[0])
	}
	if samples[4].Attributes["direction"] != "transmit" || samples[4].Value != 2000 {
		t.Fatalf("unexpected transmit byte sample: %+v", samples[4])
	}
}

func TestParsePSI(t *testing.T) {
	input := []byte("some avg10=12.50 avg60=6.00 avg300=3.00 total=123456\nfull avg10=1.00 avg60=0.50 avg300=0.25 total=456\n")
	ts := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	samples, err := parsePSI("memory", input, ts)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 8 {
		t.Fatalf("samples=%d want 8", len(samples))
	}
	if samples[0].Name != "system.pressure.stall" || samples[0].Value != 0.125 {
		t.Fatalf("unexpected avg10 sample: %+v", samples[0])
	}
	if samples[3].Name != "system.pressure.stall.time" || samples[3].Value != 123456 {
		t.Fatalf("unexpected total sample: %+v", samples[3])
	}
	if samples[4].Attributes["scope"] != "full" {
		t.Fatalf("unexpected full scope sample: %+v", samples[4])
	}
}
