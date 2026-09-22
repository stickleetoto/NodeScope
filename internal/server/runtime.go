package server

import (
	"net/http"
	"sync/atomic"
	"time"

	"github.com/jjp-monitor/jjp/internal/protocol"
)

type runtimeStats struct {
	started          time.Time
	requests         atomic.Uint64
	clientErrors     atomic.Uint64
	serverErrors     atomic.Uint64
	requestNanos     atomic.Uint64
	requestMaxNanos  atomic.Uint64
	heartbeats       atomic.Uint64
	telemetryBatches atomic.Uint64
	telemetrySamples atomic.Uint64
}

func newRuntimeStats() *runtimeStats {
	return &runtimeStats{started: time.Now().UTC()}
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func withRuntimeStats(stats *runtimeStats, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		if sw.status == 0 {
			sw.status = http.StatusOK
		}
		elapsed := uint64(time.Since(start))
		stats.requests.Add(1)
		stats.requestNanos.Add(elapsed)
		for {
			old := stats.requestMaxNanos.Load()
			if elapsed <= old || stats.requestMaxNanos.CompareAndSwap(old, elapsed) {
				break
			}
		}
		if sw.status >= 500 {
			stats.serverErrors.Add(1)
		} else if sw.status >= 400 {
			stats.clientErrors.Add(1)
		}
	})
}

func (s *Server) systemHealthSnapshot() protocol.SystemHealth {
	now := time.Now().UTC()
	stats := s.runtime
	if stats == nil {
		stats = newRuntimeStats()
		s.runtime = stats
	}
	requests := stats.requests.Load()
	avgMS := 0.0
	if requests > 0 {
		avgMS = float64(stats.requestNanos.Load()) / float64(requests) / float64(time.Millisecond)
	}
	out := protocol.SystemHealth{
		GeneratedAt:           now,
		UptimeSeconds:         uint64(now.Sub(stats.started).Seconds()),
		RequestsTotal:         requests,
		ClientErrorsTotal:     stats.clientErrors.Load(),
		ServerErrorsTotal:     stats.serverErrors.Load(),
		RequestAverageMS:      avgMS,
		RequestMaxMS:          float64(stats.requestMaxNanos.Load()) / float64(time.Millisecond),
		HeartbeatsTotal:       stats.heartbeats.Load(),
		TelemetryBatchesTotal: stats.telemetryBatches.Load(),
		TelemetrySamplesTotal: stats.telemetrySamples.Load(),
	}
	if s.Store != nil {
		views := s.Store.Views(now)
		out.NodesTotal = len(views)
		for _, node := range views {
			if node.Status == "ONLINE" {
				out.NodesOnline++
			}
		}
	}
	if s.History != nil {
		if hs, err := s.History.Stats(); err == nil {
			out.HistoryBytes = hs.Bytes
			out.HistoryMaxBytes = hs.MaxBytes
			out.HistoryRawSegments = hs.Segments
			out.HistoryRollupFiles = hs.RollupFiles
			out.HistoryRollupBytes = hs.RollupBytes
		}
	}
	return out
}
