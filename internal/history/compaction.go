package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jjp-monitor/jjp/internal/atomicfile"
)

func (s *Store) Compact(now time.Time) error {
	now = now.UTC()
	s.mu.Lock()
	defer s.mu.Unlock()

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	cutoff := now.Add(-s.opts.RawRetention)
	type candidate struct {
		name string
		path string
		mod  time.Time
	}
	var segments []candidate
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "segment-") || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if info.ModTime().UTC().After(cutoff) {
			continue
		}
		segments = append(segments, candidate{name: e.Name(), path: filepath.Join(s.dir, e.Name()), mod: info.ModTime().UTC()})
	}
	sort.Slice(segments, func(i, j int) bool {
		if segments[i].mod.Equal(segments[j].mod) {
			return segments[i].name < segments[j].name
		}
		return segments[i].mod.Before(segments[j].mod)
	})

	for _, seg := range segments {
		outName := rollupNameForSegment(seg.name)
		if outName == "" {
			continue
		}
		outPath := filepath.Join(s.dir, outName)
		if _, err := os.Stat(outPath); err == nil {
			if err := os.Remove(seg.path); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		} else if !os.IsNotExist(err) {
			return err
		}

		buckets, err := rollupRawFile(seg.path, time.Minute)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(buckets)
		if err != nil {
			return err
		}
		if err := atomicfile.WriteFile(outPath, encoded, 0o600); err != nil {
			return err
		}
		if err := os.Remove(seg.path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}

	if err := s.pruneExpiredRollupsLocked(now); err != nil {
		return err
	}
	return s.pruneLocked()
}

func rollupRawFile(path string, bucket time.Duration) ([]RollupBucket, error) {
	accs := map[string]*rollupAcc{}
	if err := scanRecords(path, func(rec record) error {
		for _, sample := range rec.Samples {
			addSampleToRollup(accs, rec.NodeID, sample, bucket)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return finalizeRollups(accs), nil
}

func rollupNameForSegment(name string) string {
	if !strings.HasPrefix(name, "segment-") || !strings.HasSuffix(name, ".jsonl") {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(name, "segment-"), ".jsonl")
	return "rollup-1m-" + id + ".json"
}

func segmentNameForRollup(name string) string {
	if !strings.HasPrefix(name, "rollup-1m-") || !strings.HasSuffix(name, ".json") {
		return ""
	}
	id := strings.TrimSuffix(strings.TrimPrefix(name, "rollup-1m-"), ".json")
	return "segment-" + id + ".jsonl"
}

func (s *Store) pruneExpiredRollupsLocked(now time.Time) error {
	cutoff := now.Add(-s.opts.RollupRetention)
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "rollup-1m-") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if info.ModTime().UTC().Before(cutoff) {
			if err := os.Remove(filepath.Join(s.dir, e.Name())); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func loadRollupFile(path string) ([]RollupBucket, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []RollupBucket
	if len(b) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("decode rollup %s: %w", filepath.Base(path), err)
	}
	return out, nil
}
