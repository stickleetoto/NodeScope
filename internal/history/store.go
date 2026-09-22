package history

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jjp-monitor/jjp/internal/atomicfile"
	"github.com/jjp-monitor/jjp/internal/protocol"
)

const (
	defaultMaxSegmentBytes = 8 << 20
	defaultMaxTotalBytes   = 256 << 20
	maxBatchSamples        = 4096
	maxRecordBytes         = 4 << 20
)

type Options struct {
	MaxSegmentBytes int64
	MaxTotalBytes   int64
}

func DefaultOptions() Options {
	return Options{MaxSegmentBytes: defaultMaxSegmentBytes, MaxTotalBytes: defaultMaxTotalBytes}
}

type record struct {
	NodeID   string                     `json:"node_id"`
	Sequence uint64                     `json:"sequence"`
	Samples  []protocol.TelemetrySample `json:"samples"`
}

type Point struct {
	NodeID   string                   `json:"node_id"`
	Sequence uint64                   `json:"sequence"`
	Sample   protocol.TelemetrySample `json:"sample"`
}

type Query struct {
	NodeID string
	Metric string
	Since  time.Time
	Until  time.Time
	Limit  int
}

type Stats struct {
	Bytes      int64             `json:"bytes"`
	Segments   int               `json:"segments"`
	NodeAcks   map[string]uint64 `json:"node_acks"`
	HeadBytes  int64             `json:"head_bytes"`
	MaxBytes   int64             `json:"max_bytes"`
	MaxSegment int64             `json:"max_segment_bytes"`
}

type Store struct {
	mu     sync.RWMutex
	dir    string
	opts   Options
	latest map[string]uint64
}

func Open(dir string, opts Options) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, fmt.Errorf("history directory is required")
	}
	if opts.MaxSegmentBytes <= 0 {
		opts.MaxSegmentBytes = defaultMaxSegmentBytes
	}
	if opts.MaxTotalBytes <= 0 {
		opts.MaxTotalBytes = defaultMaxTotalBytes
	}
	if opts.MaxTotalBytes < opts.MaxSegmentBytes {
		return nil, fmt.Errorf("history max total bytes must be >= max segment bytes")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, opts: opts, latest: map[string]uint64{}}
	if err := s.loadAcks(); err != nil {
		return nil, err
	}
	if err := s.recoverLatestFromData(); err != nil {
		return nil, err
	}
	if err := s.persistAcksLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Append(nodeID string, batch protocol.TelemetryBatch) (protocol.TelemetryAck, error) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return protocol.TelemetryAck{}, fmt.Errorf("node id is required")
	}
	if batch.Sequence == 0 {
		return protocol.TelemetryAck{}, fmt.Errorf("telemetry sequence must be > 0")
	}
	if len(batch.Samples) == 0 {
		return protocol.TelemetryAck{}, fmt.Errorf("telemetry batch is empty")
	}
	if len(batch.Samples) > maxBatchSamples {
		return protocol.TelemetryAck{}, fmt.Errorf("telemetry batch exceeds %d samples", maxBatchSamples)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if batch.Sequence <= s.latest[nodeID] {
		return protocol.TelemetryAck{DurableSequence: s.latest[nodeID], AcceptedSamples: 0}, nil
	}

	rec := record{NodeID: nodeID, Sequence: batch.Sequence, Samples: batch.Samples}
	line, err := json.Marshal(rec)
	if err != nil {
		return protocol.TelemetryAck{}, err
	}
	line = append(line, '\n')
	if len(line) > maxRecordBytes {
		return protocol.TelemetryAck{}, fmt.Errorf("telemetry batch exceeds %d encoded bytes", maxRecordBytes)
	}

	head := filepath.Join(s.dir, "head.jsonl")
	if info, err := os.Stat(head); err == nil && info.Size()+int64(len(line)) > s.opts.MaxSegmentBytes {
		if err := s.rotateHeadLocked(head); err != nil {
			return protocol.TelemetryAck{}, err
		}
	} else if err != nil && !os.IsNotExist(err) {
		return protocol.TelemetryAck{}, err
	}

	f, err := os.OpenFile(head, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return protocol.TelemetryAck{}, err
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return protocol.TelemetryAck{}, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return protocol.TelemetryAck{}, err
	}
	if err := f.Close(); err != nil {
		return protocol.TelemetryAck{}, err
	}

	s.latest[nodeID] = batch.Sequence
	if err := s.persistAcksLocked(); err != nil {
		return protocol.TelemetryAck{}, err
	}
	if err := s.pruneLocked(); err != nil {
		return protocol.TelemetryAck{}, err
	}
	return protocol.TelemetryAck{DurableSequence: batch.Sequence, AcceptedSamples: len(batch.Samples)}, nil
}

func (s *Store) Query(q Query) ([]Point, error) {
	if q.Limit <= 0 {
		q.Limit = 500
	}
	if q.Limit > 5000 {
		q.Limit = 5000
	}
	q.NodeID = strings.TrimSpace(q.NodeID)
	q.Metric = strings.TrimSpace(q.Metric)
	q.Since = q.Since.UTC()
	q.Until = q.Until.UTC()

	s.mu.RLock()
	defer s.mu.RUnlock()

	files, err := s.dataFilesLocked()
	if err != nil {
		return nil, err
	}
	ring := make([]Point, q.Limit)
	total := 0
	for _, path := range files {
		err := scanRecords(path, func(rec record) error {
			if q.NodeID != "" && rec.NodeID != q.NodeID {
				return nil
			}
			for _, sample := range rec.Samples {
				if q.Metric != "" && sample.Name != q.Metric {
					continue
				}
				ts := sample.Timestamp.UTC()
				if !q.Since.IsZero() && ts.Before(q.Since) {
					continue
				}
				if !q.Until.IsZero() && ts.After(q.Until) {
					continue
				}
				ring[total%q.Limit] = Point{NodeID: rec.NodeID, Sequence: rec.Sequence, Sample: sample}
				total++
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if total == 0 {
		return []Point{}, nil
	}
	n := total
	if n > q.Limit {
		n = q.Limit
	}
	out := make([]Point, 0, n)
	start := 0
	if total > q.Limit {
		start = total % q.Limit
	}
	for i := 0; i < n; i++ {
		out = append(out, ring[(start+i)%q.Limit])
	}
	return out, nil
}

func (s *Store) Stats() (Stats, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return Stats{}, err
	}
	out := Stats{
		NodeAcks:   make(map[string]uint64, len(s.latest)),
		MaxBytes:   s.opts.MaxTotalBytes,
		MaxSegment: s.opts.MaxSegmentBytes,
	}
	for k, v := range s.latest {
		out.NodeAcks[k] = v
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return Stats{}, err
		}
		switch {
		case e.Name() == "head.jsonl":
			out.HeadBytes = info.Size()
			out.Bytes += info.Size()
		case strings.HasPrefix(e.Name(), "segment-") && strings.HasSuffix(e.Name(), ".jsonl"):
			out.Segments++
			out.Bytes += info.Size()
		}
	}
	return out, nil
}

func (s *Store) loadAcks() error {
	b, err := os.ReadFile(filepath.Join(s.dir, "acks.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var v map[string]uint64
	if err := json.Unmarshal(b, &v); err != nil {
		return fmt.Errorf("decode history acknowledgements: %w", err)
	}
	for k, seq := range v {
		if strings.TrimSpace(k) != "" && seq > s.latest[k] {
			s.latest[k] = seq
		}
	}
	return nil
}

func (s *Store) recoverLatestFromData() error {
	files, err := s.dataFilesLocked()
	if err != nil {
		return err
	}
	for _, path := range files {
		if err := scanRecords(path, func(rec record) error {
			if rec.NodeID != "" && rec.Sequence > s.latest[rec.NodeID] {
				s.latest[rec.NodeID] = rec.Sequence
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) persistAcksLocked() error {
	b, err := json.MarshalIndent(s.latest, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.WriteFile(filepath.Join(s.dir, "acks.json"), b, 0o600)
}

func (s *Store) rotateHeadLocked(head string) error {
	info, err := os.Stat(head)
	if os.IsNotExist(err) || (err == nil && info.Size() == 0) {
		return nil
	}
	if err != nil {
		return err
	}
	name := fmt.Sprintf("segment-%020d.jsonl", time.Now().UTC().UnixNano())
	return os.Rename(head, filepath.Join(s.dir, name))
}

func (s *Store) pruneLocked() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	type fileInfo struct {
		name string
		size int64
	}
	var segments []fileInfo
	var total int64
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if e.Name() == "head.jsonl" {
			total += info.Size()
			continue
		}
		if strings.HasPrefix(e.Name(), "segment-") && strings.HasSuffix(e.Name(), ".jsonl") {
			segments = append(segments, fileInfo{name: e.Name(), size: info.Size()})
			total += info.Size()
		}
	}
	sort.Slice(segments, func(i, j int) bool { return segments[i].name < segments[j].name })
	for _, seg := range segments {
		if total <= s.opts.MaxTotalBytes {
			break
		}
		if err := os.Remove(filepath.Join(s.dir, seg.name)); err != nil && !os.IsNotExist(err) {
			return err
		}
		total -= seg.size
	}
	return nil
}

func (s *Store) dataFilesLocked() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var segments []string
	head := ""
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), "segment-") && strings.HasSuffix(e.Name(), ".jsonl") {
			segments = append(segments, filepath.Join(s.dir, e.Name()))
		} else if e.Name() == "head.jsonl" {
			head = filepath.Join(s.dir, e.Name())
		}
	}
	sort.Strings(segments)
	if head != "" {
		segments = append(segments, head)
	}
	return segments, nil
}

func scanRecords(path string, fn func(record) error) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			var rec record
			if decErr := json.Unmarshal(line, &rec); decErr != nil {
				if err == io.EOF {
					break
				}
				return fmt.Errorf("decode history record %s: %w", filepath.Base(path), decErr)
			}
			if fnErr := fn(rec); fnErr != nil {
				return fnErr
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	return nil
}
