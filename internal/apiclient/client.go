package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stickleetoto/NodeScope/internal/history"
	"github.com/stickleetoto/NodeScope/internal/protocol"
)

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

func New(baseURL, token string) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if !strings.Contains(baseURL, "://") {
		baseURL = "http://" + baseURL
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid server URL")
	}
	return &Client{BaseURL: u.String(), Token: strings.TrimSpace(token), HTTP: &http.Client{Timeout: 8 * time.Second}}, nil
}

func (c *Client) Info(ctx context.Context) (protocol.APIInfo, error) {
	var out protocol.APIInfo
	err := c.request(ctx, http.MethodGet, "/api/v1/info", nil, http.StatusOK, &out)
	return out, err
}

func (c *Client) Overview(ctx context.Context, events int) (protocol.Overview, error) {
	var out protocol.Overview
	if events < 0 {
		events = 0
	}
	if events > 50 {
		events = 50
	}
	path := fmt.Sprintf("/api/v1/overview?events=%d", events)
	err := c.request(ctx, http.MethodGet, path, nil, http.StatusOK, &out)
	return out, err
}

func (c *Client) Diagnosis(ctx context.Context, ref string) (protocol.Diagnosis, error) {
	var out protocol.Diagnosis
	err := c.request(ctx, http.MethodGet, "/api/v1/nodes/"+url.PathEscape(ref)+"/diagnosis", nil, http.StatusOK, &out)
	return out, err
}

func (c *Client) Summary(ctx context.Context) (protocol.Summary, error) {
	var out protocol.Summary
	err := c.request(ctx, http.MethodGet, "/api/v1/summary", nil, http.StatusOK, &out)
	return out, err
}


func (c *Client) SystemHealth(ctx context.Context) (protocol.SystemHealth, error) {
	var out protocol.SystemHealth
	err := c.request(ctx, http.MethodGet, "/api/v1/system/health", nil, http.StatusOK, &out)
	return out, err
}

func (c *Client) Nodes(ctx context.Context) ([]protocol.NodeView, error) {
	var out []protocol.NodeView
	err := c.request(ctx, http.MethodGet, "/api/v1/nodes", nil, http.StatusOK, &out)
	return out, err
}

func (c *Client) Unhealthy(ctx context.Context) ([]protocol.NodeView, error) {
	var out []protocol.NodeView
	err := c.request(ctx, http.MethodGet, "/api/v1/unhealthy", nil, http.StatusOK, &out)
	return out, err
}

func (c *Client) Alerts(ctx context.Context, ref string) ([]protocol.Alert, error) {
	var out []protocol.Alert
	path := "/api/v1/alerts"
	if strings.TrimSpace(ref) != "" {
		path += "?node=" + url.QueryEscape(ref)
	}
	err := c.request(ctx, http.MethodGet, path, nil, http.StatusOK, &out)
	return out, err
}

func (c *Client) Events(ctx context.Context, limit int, ref string) ([]protocol.Event, error) {
	return c.EventsSince(ctx, limit, ref, time.Time{})
}

func (c *Client) EventsSince(ctx context.Context, limit int, ref string, since time.Time) ([]protocol.Event, error) {
	var out []protocol.Event
	if limit <= 0 {
		limit = 50
	}
	q := url.Values{}
	q.Set("limit", fmt.Sprintf("%d", limit))
	if strings.TrimSpace(ref) != "" {
		q.Set("node", ref)
	}
	if !since.IsZero() {
		q.Set("since", since.UTC().Format(time.RFC3339))
	}
	err := c.request(ctx, http.MethodGet, "/api/v1/events?"+q.Encode(), nil, http.StatusOK, &out)
	return out, err
}

func (c *Client) Incidents(ctx context.Context, limit int, ref, status string) ([]protocol.Incident, error) {
	return c.IncidentsSince(ctx, limit, ref, status, time.Time{})
}

func (c *Client) IncidentsSince(ctx context.Context, limit int, ref, status string, since time.Time) ([]protocol.Incident, error) {
	var out []protocol.Incident
	if limit <= 0 {
		limit = 50
	}
	q := url.Values{}
	q.Set("limit", fmt.Sprintf("%d", limit))
	if strings.TrimSpace(ref) != "" {
		q.Set("node", ref)
	}
	if strings.TrimSpace(status) != "" {
		q.Set("status", status)
	}
	if !since.IsZero() {
		q.Set("since", since.UTC().Format(time.RFC3339))
	}
	err := c.request(ctx, http.MethodGet, "/api/v1/incidents?"+q.Encode(), nil, http.StatusOK, &out)
	return out, err
}

func (c *Client) Incident(ctx context.Context, id string) (protocol.IncidentDetail, error) {
	var out protocol.IncidentDetail
	err := c.request(ctx, http.MethodGet, "/api/v1/incidents/"+url.PathEscape(id), nil, http.StatusOK, &out)
	return out, err
}


func (c *Client) MetricHistory(ctx context.Context, ref, metric string, since, until time.Time, limit int) ([]history.Point, error) {
	return c.MetricHistoryFiltered(ctx, ref, metric, nil, since, until, limit)
}

func (c *Client) MetricHistoryFiltered(ctx context.Context, ref, metric string, attrs map[string]string, since, until time.Time, limit int) ([]history.Point, error) {
	var out []history.Point
	if limit <= 0 {
		limit = 500
	}
	if limit > 5000 {
		limit = 5000
	}
	q := url.Values{}
	q.Set("limit", fmt.Sprintf("%d", limit))
	if strings.TrimSpace(ref) != "" {
		q.Set("node", ref)
	}
	if strings.TrimSpace(metric) != "" {
		q.Set("metric", metric)
	}
	for k, v := range attrs {
		q.Add("attr", k+"="+v)
	}
	if !since.IsZero() {
		q.Set("since", since.UTC().Format(time.RFC3339))
	}
	if !until.IsZero() {
		q.Set("until", until.UTC().Format(time.RFC3339))
	}
	err := c.request(ctx, http.MethodGet, "/api/v1/metrics/history?"+q.Encode(), nil, http.StatusOK, &out)
	return out, err
}

func (c *Client) MetricHistoryStats(ctx context.Context) (history.Stats, error) {
	var out history.Stats
	err := c.request(ctx, http.MethodGet, "/api/v1/metrics/history/stats", nil, http.StatusOK, &out)
	return out, err
}


func (c *Client) MetricRollup(ctx context.Context, ref, metric string, since, until time.Time, bucket time.Duration) ([]history.RollupBucket, error) {
	return c.MetricRollupFiltered(ctx, ref, metric, nil, since, until, bucket)
}

func (c *Client) MetricRollupFiltered(ctx context.Context, ref, metric string, attrs map[string]string, since, until time.Time, bucket time.Duration) ([]history.RollupBucket, error) {
	var out []history.RollupBucket
	if bucket <= 0 {
		bucket = time.Minute
	}
	q := url.Values{}
	q.Set("node", ref)
	q.Set("metric", metric)
	q.Set("bucket", bucket.String())
	for k, v := range attrs {
		q.Add("attr", k+"="+v)
	}
	if !since.IsZero() {
		q.Set("since", since.UTC().Format(time.RFC3339))
	}
	if !until.IsZero() {
		q.Set("until", until.UTC().Format(time.RFC3339))
	}
	err := c.request(ctx, http.MethodGet, "/api/v1/metrics/rollup?"+q.Encode(), nil, http.StatusOK, &out)
	return out, err
}

func (c *Client) Node(ctx context.Context, ref string) (protocol.NodeView, error) {
	var out protocol.NodeView
	err := c.request(ctx, http.MethodGet, "/api/v1/nodes/"+url.PathEscape(ref), nil, http.StatusOK, &out)
	return out, err
}

func (c *Client) Services(ctx context.Context, ref string) ([]protocol.ServiceStatus, error) {
	var out []protocol.ServiceStatus
	err := c.request(ctx, http.MethodGet, "/api/v1/nodes/"+url.PathEscape(ref)+"/services", nil, http.StatusOK, &out)
	return out, err
}

func (c *Client) Rename(ctx context.Context, ref, name string) error {
	return c.request(ctx, http.MethodPatch, "/api/v1/nodes/"+url.PathEscape(ref), protocol.RenameNodeRequest{Name: name}, http.StatusNoContent, nil)
}

func (c *Client) Remove(ctx context.Context, ref string) error {
	return c.request(ctx, http.MethodDelete, "/api/v1/nodes/"+url.PathEscape(ref), nil, http.StatusNoContent, nil)
}

func (c *Client) RotateToken(ctx context.Context, kind string) (protocol.TokenRotationResponse, error) {
	var out protocol.TokenRotationResponse
	err := c.request(ctx, http.MethodPost, "/api/v1/tokens/"+url.PathEscape(kind)+"/rotate", nil, http.StatusOK, &out)
	return out, err
}

func (c *Client) request(ctx context.Context, method, path string, body any, expected int, dst any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != expected {
		var apiErr protocol.APIErrorBody
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&apiErr); err == nil && apiErr.Error.Message != "" {
			if apiErr.Error.RequestID != "" {
				return fmt.Errorf("API %s: %s (request %s)", apiErr.Error.Code, apiErr.Error.Message, apiErr.Error.RequestID)
			}
			return fmt.Errorf("API %s: %s", apiErr.Error.Code, apiErr.Error.Message)
		}
		return fmt.Errorf("server returned %s", resp.Status)
	}
	if dst != nil {
		return json.NewDecoder(resp.Body).Decode(dst)
	}
	return nil
}
