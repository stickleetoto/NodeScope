package agentwal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jjp-monitor/jjp/internal/atomicfile"
	"github.com/jjp-monitor/jjp/internal/protocol"
)

const (
	DefaultMaxBytes = 16 << 20
	maxBatchSamples = 4096
)

type Stats struct {
	PendingBatches int    `json:"pending_batches"`
	PendingSamples int    `json:"pending_samples"`
	Bytes          int64  `json:"bytes"`
	MaxBytes       int64  `json:"max_bytes"`
	DroppedBatches uint64 `json:"dropped_batches"`
	NextSequence   uint64 `json:"next_sequence"`
}

type WAL struct {
	mu      sync.Mutex
	path    string
	max     int64
	records []protocol.TelemetryBatch
	next    uint64
	dropped uint64
}

func Open(path string, maxBytes int64) (*WAL, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("telemetry WAL path is required")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	w := &WAL{path: path, max: maxBytes}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return w, nil
	}
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(b), "\n")
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var batch protocol.TelemetryBatch
		if err := json.Unmarshal([]byte(line), &batch); err != nil {
			// A crash can leave only the final append incomplete. Preserve all
			// earlier durable records and rewrite the queue without that tail.
			if i == len(lines)-1 || i == len(lines)-2 {
				break
			}
			return nil, fmt.Errorf("decode telemetry WAL: %w", err)
		}
		if batch.Sequence == 0 || len(batch.Samples) == 0 {
			continue
		}
		w.records = append(w.records, batch)
		if batch.Sequence > w.next {
			w.next = batch.Sequence
		}
	}
	if err := w.rewriteLocked(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *WAL) Enqueue(samples []protocol.TelemetrySample) (protocol.TelemetryBatch, error) {
	if len(samples) == 0 {
		return protocol.TelemetryBatch{}, fmt.Errorf("cannot enqueue empty telemetry batch")
	}
	if len(samples) > maxBatchSamples {
		return protocol.TelemetryBatch{}, fmt.Errorf("telemetry batch exceeds %d samples", maxBatchSamples)
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	batch := protocol.TelemetryBatch{Sequence: w.next + 1, Samples: append([]protocol.TelemetrySample(nil), samples...)}
	encoded, err := json.Marshal(batch)
	if err != nil {
		return protocol.TelemetryBatch{}, err
	}
	if int64(len(encoded)+1) > w.max {
		return protocol.TelemetryBatch{}, fmt.Errorf("single telemetry batch exceeds WAL capacity")
	}

	oldRecords := append([]protocol.TelemetryBatch(nil), w.records...)
	oldNext, oldDropped := w.next, w.dropped
	w.records = append(w.records, batch)
	w.next = batch.Sequence
	for {
		size, err := encodedSize(w.records)
		if err != nil {
			w.records, w.next, w.dropped = oldRecords, oldNext, oldDropped
			return protocol.TelemetryBatch{}, err
		}
		if size <= w.max || len(w.records) <= 1 {
			break
		}
		w.records = w.records[1:]
		w.dropped++
	}
	if err := w.rewriteLocked(); err != nil {
		w.records, w.next, w.dropped = oldRecords, oldNext, oldDropped
		return protocol.TelemetryBatch{}, err
	}
	return batch, nil
}

func (w *WAL) Pending(max int) []protocol.TelemetryBatch {
	w.mu.Lock()
	defer w.mu.Unlock()
	if max <= 0 || max > len(w.records) {
		max = len(w.records)
	}
	out := make([]protocol.TelemetryBatch, 0, max)
	for i := 0; i < max; i++ {
		b := w.records[i]
		b.Samples = append([]protocol.TelemetrySample(nil), b.Samples...)
		out = append(out, b)
	}
	return out
}

func (w *WAL) Ack(sequence uint64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	idx := 0
	for idx < len(w.records) && w.records[idx].Sequence <= sequence {
		idx++
	}
	if idx == 0 {
		return nil
	}
	w.records = append([]protocol.TelemetryBatch(nil), w.records[idx:]...)
	return w.rewriteLocked()
}

func (w *WAL) Stats() (Stats, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	size, err := encodedSize(w.records)
	if err != nil {
		return Stats{}, err
	}
	samples := 0
	for _, batch := range w.records {
		samples += len(batch.Samples)
	}
	return Stats{
		PendingBatches: len(w.records),
		PendingSamples: samples,
		Bytes:          size,
		MaxBytes:       w.max,
		DroppedBatches: w.dropped,
		NextSequence:   w.next + 1,
	}, nil
}

func (w *WAL) rewriteLocked() error {
	if len(w.records) == 0 {
		return atomicfile.WriteFile(w.path, nil, 0o600)
	}
	var b strings.Builder
	for _, batch := range w.records {
		line, err := json.Marshal(batch)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return atomicfile.WriteFile(w.path, []byte(b.String()), 0o600)
}

func encodedSize(records []protocol.TelemetryBatch) (int64, error) {
	var size int64
	for _, batch := range records {
		b, err := json.Marshal(batch)
		if err != nil {
			return 0, err
		}
		size += int64(len(b) + 1)
	}
	return size, nil
}
