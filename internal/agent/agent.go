package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/jjp-monitor/jjp/internal/agentwal"
	"github.com/jjp-monitor/jjp/internal/metrics"
	"github.com/jjp-monitor/jjp/internal/protocol"
	"github.com/jjp-monitor/jjp/internal/servicecheck"
)

var HTTPClient = &http.Client{Timeout: 8 * time.Second}

func NormalizeServer(v string) (string, error) {
	v = strings.TrimRight(strings.TrimSpace(v), "/")
	if !strings.Contains(v, "://") {
		v = "http://" + v
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid server address")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("server must use http or https")
	}
	return u.String(), nil
}

func Join(serverURL, token, name string) (Config, error) {
	serverURL, err := NormalizeServer(serverURL)
	if err != nil {
		return Config{}, err
	}
	req := protocol.JoinRequest{Name: name, OS: runtime.GOOS, Arch: runtime.GOARCH, AgentVersion: protocol.Version}
	b, _ := json.Marshal(req)
	httpReq, err := http.NewRequest(http.MethodPost, serverURL+"/api/v1/join", bytes.NewReader(b))
	if err != nil {
		return Config{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	resp, err := HTTPClient.Do(httpReq)
	if err != nil {
		return Config{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return Config{}, fmt.Errorf("join failed: %s", resp.Status)
	}
	var jr protocol.JoinResponse
	if err := json.NewDecoder(resp.Body).Decode(&jr); err != nil {
		return Config{}, err
	}
	return Config{Server: serverURL, NodeID: jr.NodeID, Secret: jr.Secret, Name: name}, nil
}

// Run continuously reports liveness/current state and separately queues richer
// telemetry. Telemetry survives transient central outages in a bounded local WAL.
func Run(ctx context.Context, c Config, interval time.Duration, onBeat func(protocol.Metrics, []protocol.ServiceStatus, error)) error {
	if interval < time.Second {
		interval = time.Second
	}
	walPath, err := DefaultTelemetryWALPath(c.NodeID)
	if err != nil {
		return err
	}
	telemetryWAL, err := agentwal.Open(walPath, agentwal.DefaultMaxBytes, agentwal.DefaultMaxAge)
	if err != nil {
		return fmt.Errorf("open telemetry WAL: %w", err)
	}

	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		m, collectErr := metrics.Collect()
		services := servicecheck.CheckAll(ctx, c.Services)

		var telemetryErr error
		if collectErr == nil {
			samples, collectorErrs := metrics.CollectSamplesFromLegacy(ctx, m)
			if len(collectorErrs) > 0 {
				parts := make([]string, 0, len(collectorErrs))
				for _, collectorErr := range collectorErrs {
					parts = append(parts, collectorErr.Error())
				}
				telemetryErr = fmt.Errorf("collector warnings: %s", strings.Join(parts, "; "))
			}
			if len(samples) > 0 {
				wireSamples := metrics.ToProtocolSamples(samples)
				if stats, statsErr := telemetryWAL.Stats(); statsErr == nil {
					wireSamples = append(wireSamples, walTelemetrySamples(stats)...)
				}
				if _, enqueueErr := telemetryWAL.Enqueue(wireSamples); enqueueErr != nil {
					telemetryErr = joinCycleErrors(telemetryErr, fmt.Errorf("queue telemetry: %w", enqueueErr))
				}
			}
		}

		heartbeatErr := collectErr
		if heartbeatErr == nil {
			heartbeatErr = sendHeartbeat(ctx, c, m, services)
		}

		// Telemetry failures never slow the liveness heartbeat cadence. The
		// bounded WAL absorbs history outages while heartbeat/current state can
		// keep flowing independently.
		flushErr := flushTelemetry(ctx, c, telemetryWAL)
		reportErr := joinCycleErrors(heartbeatErr, telemetryErr, flushErr)
		if onBeat != nil {
			onBeat(m, services, reportErr)
		}

		delay := interval
		if heartbeatErr != nil {
			delay = backoff
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		} else {
			backoff = time.Second
		}

		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !t.Stop() {
				<-t.C
			}
			return ctx.Err()
		case <-t.C:
		}
	}
}

func joinCycleErrors(errs ...error) error {
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		if err != nil {
			parts = append(parts, err.Error())
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}

func walTelemetrySamples(stats agentwal.Stats) []protocol.TelemetrySample {
	now := time.Now().UTC()
	utilization := 0.0
	if stats.MaxBytes > 0 {
		utilization = float64(stats.Bytes) / float64(stats.MaxBytes)
	}
	return []protocol.TelemetrySample{
		{Name: "nodescope.agent.wal.pending_batches", Value: float64(stats.PendingBatches), Unit: "{batch}", Kind: "gauge", Timestamp: now, Collector: "nodescope"},
		{Name: "nodescope.agent.wal.pending_samples", Value: float64(stats.PendingSamples), Unit: "{sample}", Kind: "gauge", Timestamp: now, Collector: "nodescope"},
		{Name: "nodescope.agent.wal.bytes", Value: float64(stats.Bytes), Unit: "By", Kind: "gauge", Timestamp: now, Collector: "nodescope"},
		{Name: "nodescope.agent.wal.utilization", Value: utilization, Unit: "1", Kind: "gauge", Timestamp: now, Collector: "nodescope"},
		{Name: "nodescope.agent.wal.dropped_batches", Value: float64(stats.DroppedBatches), Unit: "{batch}", Kind: "counter", Timestamp: now, Collector: "nodescope"},
		{Name: "nodescope.agent.wal.oldest_record_age", Value: float64(stats.OldestRecordAgeSec), Unit: "s", Kind: "gauge", Timestamp: now, Collector: "nodescope"},
		{Name: "nodescope.agent.wal.max_age", Value: float64(stats.MaxAgeSeconds), Unit: "s", Kind: "gauge", Timestamp: now, Collector: "nodescope"},
	}
}

func flushTelemetry(ctx context.Context, c Config, wal *agentwal.WAL) error {
	for _, batch := range wal.Pending(8) {
		ack, err := sendTelemetry(ctx, c, batch)
		if err != nil {
			return err
		}
		if ack.DurableSequence < batch.Sequence {
			return fmt.Errorf("telemetry ACK %d is behind batch %d", ack.DurableSequence, batch.Sequence)
		}
		if err := wal.Ack(ack.DurableSequence); err != nil {
			return fmt.Errorf("advance telemetry WAL: %w", err)
		}
	}
	return nil
}

func sendTelemetry(ctx context.Context, c Config, batch protocol.TelemetryBatch) (protocol.TelemetryAck, error) {
	payload, err := json.Marshal(batch)
	if err != nil {
		return protocol.TelemetryAck{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Server+"/api/v1/telemetry", bytes.NewReader(payload))
	if err != nil {
		return protocol.TelemetryAck{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Secret)
	req.Header.Set("X-JJP-Node-ID", c.NodeID)
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return protocol.TelemetryAck{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return protocol.TelemetryAck{}, fmt.Errorf("telemetry upload failed: %s", resp.Status)
	}
	var ack protocol.TelemetryAck
	if err := json.NewDecoder(resp.Body).Decode(&ack); err != nil {
		return protocol.TelemetryAck{}, fmt.Errorf("decode telemetry ACK: %w", err)
	}
	return ack, nil
}

func sendHeartbeat(ctx context.Context, c Config, m protocol.Metrics, services []protocol.ServiceStatus) error {
	payload, _ := json.Marshal(protocol.HeartbeatRequest{Metrics: m, Services: services})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Server+"/api/v1/heartbeat", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Secret)
	req.Header.Set("X-JJP-Node-ID", c.NodeID)
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("heartbeat failed: %s", resp.Status)
	}
	return nil
}
