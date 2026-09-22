package history

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jjp-monitor/jjp/internal/protocol"
)

const maxRollupBuckets = 10000

type RollupBucket struct {
	NodeID     string            `json:"node_id"`
	Metric     string            `json:"metric"`
	Attributes map[string]string `json:"attributes,omitempty"`
	Unit       string            `json:"unit,omitempty"`
	Kind       string            `json:"kind"`
	Start      time.Time         `json:"start"`
	End        time.Time         `json:"end"`
	Count      int               `json:"count"`
	Min        float64           `json:"min"`
	Max        float64           `json:"max"`
	Average    float64           `json:"average"`
	First      float64           `json:"first"`
	Last       float64           `json:"last"`
}

type rollupAcc struct {
	RollupBucket
	sum       float64
	firstTime time.Time
	lastTime  time.Time
}

func (s *Store) Rollup(q Query, bucket time.Duration) ([]RollupBucket, error) {
	if bucket < time.Minute || bucket > 24*time.Hour {
		return nil, fmt.Errorf("rollup bucket must be between 1m and 24h")
	}
	q.NodeID = strings.TrimSpace(q.NodeID)
	q.Metric = strings.TrimSpace(q.Metric)
	q.Since = q.Since.UTC()
	q.Until = q.Until.UTC()

	s.mu.RLock()
	defer s.mu.RUnlock()

	accs := map[string]*rollupAcc{}
	compactedRaw := map[string]bool{}

	rollupFiles, err := s.rollupFilesLocked()
	if err != nil {
		return nil, err
	}
	for _, path := range rollupFiles {
		buckets, err := loadRollupFile(path)
		if err != nil {
			return nil, err
		}
		compactedRaw[segmentNameForRollup(filepath.Base(path))] = true
		for _, source := range buckets {
			if q.NodeID != "" && source.NodeID != q.NodeID {
				continue
			}
			if q.Metric != "" && source.Metric != q.Metric {
				continue
			}
			if !matchesAttributes(source.Attributes, q.Attributes) {
				continue
			}
			if !q.Since.IsZero() && !source.End.After(q.Since) {
				continue
			}
			if !q.Until.IsZero() && source.Start.After(q.Until) {
				continue
			}
			addBucketToRollup(accs, source, bucket)
			if len(accs) > maxRollupBuckets {
				return nil, fmt.Errorf("rollup result exceeds %d buckets; narrow the time window or metric filter", maxRollupBuckets)
			}
		}
	}

	rawFiles, err := s.dataFilesLocked()
	if err != nil {
		return nil, err
	}
	for _, path := range rawFiles {
		base := filepath.Base(path)
		if compactedRaw[base] {
			continue
		}
		if err := scanRecords(path, func(rec record) error {
			if q.NodeID != "" && rec.NodeID != q.NodeID {
				return nil
			}
			for _, sample := range rec.Samples {
				if q.Metric != "" && sample.Name != q.Metric {
					continue
				}
				if !matchesAttributes(sample.Attributes, q.Attributes) {
					continue
				}
				ts := sample.Timestamp.UTC()
				if !q.Since.IsZero() && ts.Before(q.Since) {
					continue
				}
				if !q.Until.IsZero() && ts.After(q.Until) {
					continue
				}
				addSampleToRollup(accs, rec.NodeID, sample, bucket)
				if len(accs) > maxRollupBuckets {
					return fmt.Errorf("rollup result exceeds %d buckets; narrow the time window or metric filter", maxRollupBuckets)
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return finalizeRollups(accs), nil
}

func addSampleToRollup(accs map[string]*rollupAcc, nodeID string, sample protocol.TelemetrySample, bucket time.Duration) {
	ts := sample.Timestamp.UTC()
	start := ts.Truncate(bucket)
	key := rollupKey(nodeID, sample.Name, sample.Attributes, start)
	a := accs[key]
	if a == nil {
		attrs := cloneAttrs(sample.Attributes)
		accs[key] = &rollupAcc{
			RollupBucket: RollupBucket{
				NodeID: nodeID, Metric: sample.Name, Attributes: attrs,
				Unit: sample.Unit, Kind: sample.Kind, Start: start, End: start.Add(bucket),
				Count: 1, Min: sample.Value, Max: sample.Value, First: sample.Value, Last: sample.Value,
			},
			sum: sample.Value, firstTime: ts, lastTime: ts,
		}
		return
	}
	a.Count++
	a.sum += sample.Value
	if sample.Value < a.Min {
		a.Min = sample.Value
	}
	if sample.Value > a.Max {
		a.Max = sample.Value
	}
	if ts.Before(a.firstTime) {
		a.firstTime = ts
		a.First = sample.Value
	}
	if ts.After(a.lastTime) || ts.Equal(a.lastTime) {
		a.lastTime = ts
		a.Last = sample.Value
	}
}

func addBucketToRollup(accs map[string]*rollupAcc, source RollupBucket, bucket time.Duration) {
	start := source.Start.UTC().Truncate(bucket)
	key := rollupKey(source.NodeID, source.Metric, source.Attributes, start)
	a := accs[key]
	if a == nil {
		copyBucket := source
		copyBucket.Attributes = cloneAttrs(source.Attributes)
		copyBucket.Start = start
		copyBucket.End = start.Add(bucket)
		accs[key] = &rollupAcc{
			RollupBucket: copyBucket,
			sum: source.Average * float64(source.Count),
			firstTime: source.Start.UTC(),
			lastTime: source.End.UTC(),
		}
		return
	}
	a.Count += source.Count
	a.sum += source.Average * float64(source.Count)
	if source.Min < a.Min {
		a.Min = source.Min
	}
	if source.Max > a.Max {
		a.Max = source.Max
	}
	if source.Start.UTC().Before(a.firstTime) {
		a.firstTime = source.Start.UTC()
		a.First = source.First
	}
	if source.End.UTC().After(a.lastTime) || source.End.UTC().Equal(a.lastTime) {
		a.lastTime = source.End.UTC()
		a.Last = source.Last
	}
}

func finalizeRollups(accs map[string]*rollupAcc) []RollupBucket {
	out := make([]RollupBucket, 0, len(accs))
	for _, a := range accs {
		if a.Count > 0 {
			a.Average = a.sum / float64(a.Count)
		}
		out = append(out, a.RollupBucket)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Start.Equal(out[j].Start) {
			return out[i].Start.Before(out[j].Start)
		}
		if out[i].NodeID != out[j].NodeID {
			return out[i].NodeID < out[j].NodeID
		}
		if out[i].Metric != out[j].Metric {
			return out[i].Metric < out[j].Metric
		}
		return canonicalAttrs(out[i].Attributes) < canonicalAttrs(out[j].Attributes)
	})
	return out
}

func (s *Store) rollupFilesLocked() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), "rollup-1m-") && strings.HasSuffix(e.Name(), ".json") {
			out = append(out, filepath.Join(s.dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

func rollupKey(nodeID, metric string, attrs map[string]string, start time.Time) string {
	return nodeID + "|" + metric + "|" + canonicalAttrs(attrs) + "|" + strconv.FormatInt(start.UnixNano(), 10)
}

func canonicalSeriesKey(sample protocol.TelemetrySample) string {
	return sample.Name + "|" + canonicalAttrs(sample.Attributes)
}

func canonicalAttrs(attrs map[string]string) string {
	if len(attrs) == 0 {
		return ""
	}
	keys := make([]string, 0, len(attrs))
	for k := range attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		v := attrs[k]
		b.WriteString(strconv.Itoa(len(k)))
		b.WriteByte(':')
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(strconv.Itoa(len(v)))
		b.WriteByte(':')
		b.WriteString(v)
		b.WriteByte(';')
	}
	return b.String()
}

func cloneAttrs(attrs map[string]string) map[string]string {
	if len(attrs) == 0 {
		return nil
	}
	out := make(map[string]string, len(attrs))
	for k, v := range attrs {
		out[k] = v
	}
	return out
}
