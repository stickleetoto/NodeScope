package history

import (
	"fmt"
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
	sum float64
	key string
}

func (s *Store) Rollup(q Query, bucket time.Duration) ([]RollupBucket, error) {
	if bucket < time.Second || bucket > 24*time.Hour {
		return nil, fmt.Errorf("rollup bucket must be between 1s and 24h")
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
	accs := map[string]*rollupAcc{}
	for _, path := range files {
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
				start := ts.Truncate(bucket)
				series := canonicalSeriesKey(sample)
				key := rec.NodeID + "|" + series + "|" + strconv.FormatInt(start.UnixNano(), 10)
				a := accs[key]
				if a == nil {
					attrs := cloneAttrs(sample.Attributes)
					a = &rollupAcc{RollupBucket: RollupBucket{
						NodeID: rec.NodeID, Metric: sample.Name, Attributes: attrs,
						Unit: sample.Unit, Kind: sample.Kind, Start: start, End: start.Add(bucket),
						Count: 1, Min: sample.Value, Max: sample.Value, First: sample.Value, Last: sample.Value,
					}, sum: sample.Value, key: series}
					accs[key] = a
				} else {
					a.Count++
					a.sum += sample.Value
					if sample.Value < a.Min {
						a.Min = sample.Value
					}
					if sample.Value > a.Max {
						a.Max = sample.Value
					}
					a.Last = sample.Value
				}
				if len(accs) > maxRollupBuckets {
					return fmt.Errorf("rollup result exceeds %d buckets; narrow the time window or metric filter", maxRollupBuckets)
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}

	out := make([]RollupBucket, 0, len(accs))
	for _, a := range accs {
		a.Average = a.sum / float64(a.Count)
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
	return out, nil
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
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(attrs[k])
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
