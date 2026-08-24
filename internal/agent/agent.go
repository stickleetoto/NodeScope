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

// Run continuously reports metrics and service health. Network failures do not
// terminate the agent; failed heartbeats are retried with exponential backoff.
func Run(ctx context.Context, c Config, interval time.Duration, onBeat func(protocol.Metrics, []protocol.ServiceStatus, error)) error {
	if interval < time.Second {
		interval = time.Second
	}
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		m, err := metrics.Collect()
		services := servicecheck.CheckAll(ctx, c.Services)
		if err == nil {
			err = sendHeartbeat(ctx, c, m, services)
		}
		if onBeat != nil {
			onBeat(m, services, err)
		}

		delay := interval
		if err != nil {
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
