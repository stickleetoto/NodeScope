package agentwal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jjp-monitor/jjp/internal/atomicfile"
	"github.com/jjp-monitor/jjp/internal/protocol"
)

const (
	DefaultMaxBytes = 16 << 20
	DefaultMaxAge   = 6 * time.Hour
	maxBatchSamples = 4096
)

type Stats struct {
	PendingBatches int    `json:"pending_batches"`
	PendingSamples int    `json:"pending_samples"`
	Bytes          int64  `json:"bytes"`
	MaxBytes       int64  `json:"max_bytes"`
	DroppedBatches       uint64 `json:"dropped_batches"`
	NextSequence         uint64 `json:"next_sequence"`
	MaxAgeSeconds        int64  `json:"max_age_seconds"`
	OldestRecordAgeSec   int64  `json:"oldest_record_age_seconds"`
}

type walMeta struct {
	LastSequence uint64 `json:"last_sequence"`
}

type WAL struct {
	mu      sync.Mutex
	path    string
	max     int64
	maxAge  time.Duration
	records []protocol.TelemetryBatch
	next    uint64
	dropped uint64
}

func Open(path string, maxBytes int64, maxAge time.Duration) (*WAL, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("telemetry WAL path is required")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	if maxAge <= 0 {
		maxAge = DefaultMaxAge
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	w := &WAL{path: path, max: maxBytes, maxAge: maxAge}
	if err := w.loadMeta(); err != nil {
		return nil, err
	}

	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
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
	}
	w.pruneExpiredLocked(time.Now().UTC())
	if err := w.rewriteLocked(); err != nil {
		return nil, err
	}
	if err := w.persistMetaLocked(); err != nil {
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

	// Persist the sequence reservation before the queue record. A crash or queue
	// write failure may create a harmless sequence gap, but can never cause
	// sequence reuse after all pending records have been ACKed.
	w.next = batch.Sequence
	if err := w.persistMetaLocked(); err != nil {
		w.next = oldNext
		return protocol.TelemetryBatch{}, err
	}

	w.records = append(w.records, batch)
	w.pruneExpiredLocked(time.Now().UTC())
	for {
		size, err := encodedSize(w.records)
		if err != nil {
			w.records, w.dropped = oldRecords, oldDropped
			return protocol.TelemetryBatch{}, err
		}
		if size <= w.max || len(w.records) <= 1 {
			break
		}
		w.records = w.records[1:]
		w.dropped++
	}
	if err := w.rewriteLocked(); err != nil {
		w.records, w.dropped = oldRecords, oldDropped
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
	oldRecords := append([]protocol.TelemetryBatch(nil), w.records...)
	w.records = append([]protocol.TelemetryBatch(nil), w.records[idx:]...)
	if err := w.rewriteLocked(); err != nil {
		w.records = oldRecords
		return err
	}
	return nil
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
	oldestAge := int64(0)
	if len(w.records) > 0 {
		if ts := batchNewestTimestamp(w.records[0]); !ts.IsZero() {
			age := time.Since(ts)
			if age > 0 {
				oldestAge = int64(age.Seconds())
			}
		}
	}
	return Stats{
		PendingBatches:     len(w.records),
		PendingSamples:     samples,
		Bytes:              size,
		MaxBytes:           w.max,
		DroppedBatches:     w.dropped,
		NextSequence:       w.next + 1,
		MaxAgeSeconds:      int64(w.maxAge.Seconds()),
		OldestRecordAgeSec: oldestAge,
	}, nil
}

func (w *WAL) pruneExpiredLocked(now time.Time) {
	if w.maxAge <= 0 || len(w.records) == 0 {
		return
	}
	cutoff := now.Add(-w.maxAge)
	idx := 0
	for idx < len(w.records) {
		ts := batchNewestTimestamp(w.records[idx])
		if ts.IsZero() || !ts.Before(cutoff) {
			break
		}
		idx++
	}
	if idx > 0 {
		w.records = append([]protocol.TelemetryBatch(nil), w.records[idx:]...)
		w.dropped += uint64(idx)
	}
}

func batchNewestTimestamp(batch protocol.TelemetryBatch) time.Time {
	var newest time.Time
	for _, sample := range batch.Samples {
		ts := sample.Timestamp.UTC()
		if ts.After(newest) {
			newest = ts
		}
	}
	return newest
}

func (w *WAL) metaPath() string {
	return w.path + ".meta.json"
}

func (w *WAL) loadMeta() error {
	b, err := os.ReadFile(w.metaPath())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var meta walMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		return fmt.Errorf("decode telemetry WAL metadata: %w", err)
	}
	w.next = meta.LastSequence
	return nil
}

func (w *WAL) persistMetaLocked() error {
	b, err := json.Marshal(walMeta{LastSequence: w.next})
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(w.metaPath(), b, 0o600)
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
