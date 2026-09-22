package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jjp-monitor/jjp/internal/history"
	"github.com/jjp-monitor/jjp/internal/insight"
	"github.com/jjp-monitor/jjp/internal/protocol"
	"github.com/jjp-monitor/jjp/internal/store"
)

type Server struct {
	Store   *store.Store
	History *history.Store
}

func RandomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]string{"status": "ok", "version": protocol.Version})
	})
	mux.HandleFunc("GET /api/v1/info", s.info)
	mux.HandleFunc("POST /api/v1/tokens/{kind}/rotate", s.rotateToken)
	mux.HandleFunc("POST /api/v1/join", s.join)
	mux.HandleFunc("POST /api/v1/heartbeat", s.heartbeat)
	mux.HandleFunc("POST /api/v1/telemetry", s.telemetry)
	mux.HandleFunc("GET /api/v1/metrics/history", s.metricHistory)
	mux.HandleFunc("GET /api/v1/metrics/history/stats", s.metricHistoryStats)
	mux.HandleFunc("GET /api/v1/metrics/rollup", s.metricRollup)
	mux.HandleFunc("GET /api/v1/summary", s.summary)
	mux.HandleFunc("GET /api/v1/overview", s.overview)
	mux.HandleFunc("GET /api/v1/unhealthy", s.unhealthy)
	mux.HandleFunc("GET /api/v1/alerts", s.alerts)
	mux.HandleFunc("GET /api/v1/events", s.events)
	mux.HandleFunc("GET /api/v1/incidents", s.incidents)
	mux.HandleFunc("GET /api/v1/incidents/{id}", s.incident)
	mux.HandleFunc("GET /api/v1/nodes", s.nodes)
	mux.HandleFunc("GET /api/v1/nodes/{id}", s.node)
	mux.HandleFunc("GET /api/v1/nodes/{id}/services", s.services)
	mux.HandleFunc("GET /api/v1/nodes/{id}/diagnosis", s.diagnosis)
	mux.HandleFunc("PATCH /api/v1/nodes/{id}", s.renameNode)
	mux.HandleFunc("DELETE /api/v1/nodes/{id}", s.deleteNode)
	return withAPIHeaders(withLimits(mux))
}

func withLimits(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(128 << 10)
		if r.URL.Path == "/api/v1/telemetry" {
			limit = 4 << 20
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

func withAPIHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-JJP-Version", protocol.Version)
		w.Header().Set("X-JJP-API-Version", protocol.APIVersion)
		w.Header().Set("X-Request-ID", requestID())
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func requestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, protocol.APIInfo{Name: "nodescope", Version: protocol.Version, APIVersion: protocol.APIVersion, StateSchema: s.Store.SchemaVersion()})
}

func (s *Server) join(w http.ResponseWriter, r *http.Request) {
	var req protocol.JoinRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, 400, "invalid_json", "invalid JSON request")
		return
	}
	joinToken := bearer(r.Header.Get("Authorization"))
	if joinToken == "" { // v0.2 compatibility
		joinToken = req.Token
	}
	if subtle.ConstantTimeCompare([]byte(joinToken), []byte(s.Store.BootstrapToken())) != 1 {
		writeAPIError(w, 401, "invalid_join_token", "invalid join token")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if err := store.ValidateNodeName(req.Name); err != nil {
		writeAPIError(w, 400, "invalid_node_name", err.Error())
		return
	}
	if len(strings.TrimSpace(req.OS)) > 64 || len(strings.TrimSpace(req.Arch)) > 64 || len(strings.TrimSpace(req.AgentVersion)) > 64 {
		writeAPIError(w, 400, "invalid_agent_identity", "os, arch and agent_version must be at most 64 characters")
		return
	}
	id, err := RandomToken(9)
	if err != nil {
		writeAPIError(w, 500, "token_generation_failed", "token generation failed")
		return
	}
	secret, err := RandomToken(32)
	if err != nil {
		writeAPIError(w, 500, "token_generation_failed", "token generation failed")
		return
	}
	now := time.Now().UTC()
	n := &protocol.Node{ID: id, Name: req.Name, OS: req.OS, Arch: req.Arch, AgentVersion: req.AgentVersion, Secret: secret, RegisteredAt: now}
	if err := s.Store.AddNode(n); err != nil {
		switch {
		case errors.Is(err, store.ErrNameExists):
			writeAPIError(w, http.StatusConflict, "node_name_exists", "node name already exists")
		case errors.Is(err, store.ErrNodeLimit):
			writeAPIError(w, http.StatusConflict, "node_limit_reached", "node limit reached")
		default:
			writeAPIError(w, 500, "storage_failure", "storage failure")
		}
		return
	}
	writeJSON(w, 201, protocol.JoinResponse{NodeID: id, Secret: secret})
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	id := r.Header.Get("X-JJP-Node-ID")
	secret := bearer(r.Header.Get("Authorization"))
	if id == "" || secret == "" {
		writeAPIError(w, 401, "missing_node_credentials", "missing node credentials")
		return
	}
	if _, ok := s.Store.AuthenticateNode(id, secret); !ok {
		writeAPIError(w, 401, "invalid_node_credentials", "invalid node credentials")
		return
	}
	var req protocol.HeartbeatRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, 400, "invalid_json", "invalid JSON request")
		return
	}
	if len(req.Services) > 128 {
		writeAPIError(w, 400, "too_many_services", "too many services")
		return
	}
	if err := validateHeartbeat(req); err != nil {
		writeAPIError(w, 400, "invalid_heartbeat", err.Error())
		return
	}
	if err := s.Store.Heartbeat(id, req.Metrics, req.Services, time.Now().UTC()); err != nil {
		writeAPIError(w, 500, "storage_failure", "storage failure")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) telemetry(w http.ResponseWriter, r *http.Request) {
	if s.History == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "history_unavailable", "telemetry history is unavailable")
		return
	}
	id := strings.TrimSpace(r.Header.Get("X-JJP-Node-ID"))
	secret := bearer(r.Header.Get("Authorization"))
	if id == "" || secret == "" {
		writeAPIError(w, http.StatusUnauthorized, "missing_node_credentials", "missing node credentials")
		return
	}
	if _, ok := s.Store.AuthenticateNode(id, secret); !ok {
		writeAPIError(w, http.StatusUnauthorized, "invalid_node_credentials", "invalid node credentials")
		return
	}
	var req protocol.TelemetryBatch
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "invalid JSON request")
		return
	}
	if err := validateTelemetryBatch(req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_telemetry", err.Error())
		return
	}
	ack, err := s.History.Append(id, req)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "history_storage_failure", "failed to persist telemetry")
		return
	}
	writeJSON(w, http.StatusOK, ack)
}

func (s *Server) metricHistory(w http.ResponseWriter, r *http.Request) {
	if !s.readOK(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "read or admin token required")
		return
	}
	if s.History == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "history_unavailable", "telemetry history is unavailable")
		return
	}

	nodeRef := strings.TrimSpace(r.URL.Query().Get("node"))
	nodeID := ""
	if nodeRef != "" {
		v, ok := s.Store.ViewByNameOrID(nodeRef, time.Now().UTC())
		if !ok {
			writeAPIError(w, http.StatusNotFound, "node_not_found", "node not found")
			return
		}
		nodeID = v.ID
	}

	since, err := parseHistoryTime("since", r.URL.Query().Get("since"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_since", err.Error())
		return
	}
	until, err := parseHistoryTime("until", r.URL.Query().Get("until"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_until", err.Error())
		return
	}
	if !since.IsZero() && !until.IsZero() && until.Before(since) {
		writeAPIError(w, http.StatusBadRequest, "invalid_window", "until must be at or after since")
		return
	}

	limit := 500
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 5000 {
			writeAPIError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 5000")
			return
		}
		limit = v
	}

	attrs, err := parseHistoryAttributes(r.URL.Query()["attr"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_attribute_filter", err.Error())
		return
	}
	points, err := s.History.Query(history.Query{
		NodeID:     nodeID,
		Metric:     strings.TrimSpace(r.URL.Query().Get("metric")),
		Attributes: attrs,
		Since:      since,
		Until:      until,
		Limit:      limit,
	})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "history_query_failure", "failed to query telemetry history")
		return
	}
	writeJSON(w, http.StatusOK, points)
}

func (s *Server) metricRollup(w http.ResponseWriter, r *http.Request) {
	if !s.readOK(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "read or admin token required")
		return
	}
	if s.History == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "history_unavailable", "telemetry history is unavailable")
		return
	}
	nodeRef := strings.TrimSpace(r.URL.Query().Get("node"))
	metric := strings.TrimSpace(r.URL.Query().Get("metric"))
	if nodeRef == "" || metric == "" {
		writeAPIError(w, http.StatusBadRequest, "missing_filter", "node and metric are required")
		return
	}
	v, ok := s.Store.ViewByNameOrID(nodeRef, time.Now().UTC())
	if !ok {
		writeAPIError(w, http.StatusNotFound, "node_not_found", "node not found")
		return
	}
	bucketRaw := strings.TrimSpace(r.URL.Query().Get("bucket"))
	if bucketRaw == "" {
		bucketRaw = "1m"
	}
	bucket, err := time.ParseDuration(bucketRaw)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_bucket", "bucket must be a valid duration")
		return
	}
	since, err := parseHistoryTime("since", r.URL.Query().Get("since"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_since", err.Error())
		return
	}
	until, err := parseHistoryTime("until", r.URL.Query().Get("until"))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_until", err.Error())
		return
	}
	if !since.IsZero() && !until.IsZero() && until.Before(since) {
		writeAPIError(w, http.StatusBadRequest, "invalid_window", "until must be at or after since")
		return
	}
	attrs, err := parseHistoryAttributes(r.URL.Query()["attr"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_attribute_filter", err.Error())
		return
	}
	out, err := s.History.Rollup(history.Query{NodeID: v.ID, Metric: metric, Attributes: attrs, Since: since, Until: until}, bucket)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "rollup_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) metricHistoryStats(w http.ResponseWriter, r *http.Request) {
	if !s.readOK(r) {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "read or admin token required")
		return
	}
	if s.History == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "history_unavailable", "telemetry history is unavailable")
		return
	}
	stats, err := s.History.Stats()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "history_stats_failure", "failed to inspect telemetry history")
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func parseHistoryAttributes(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > 8 {
		return nil, fmt.Errorf("at most 8 attribute filters are allowed")
	}
	out := make(map[string]string, len(values))
	for _, raw := range values {
		k, v, ok := strings.Cut(raw, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" || len(k) > 64 || len(v) > 128 {
			return nil, fmt.Errorf("attribute filters must use key=value with bounded lengths")
		}
		out[k] = v
	}
	return out, nil
}

func parseHistoryTime(name, raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	v, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s must be an RFC3339 timestamp", name)
	}
	return v.UTC(), nil
}

func validateTelemetryBatch(req protocol.TelemetryBatch) error {
	if req.Sequence == 0 {
		return fmt.Errorf("sequence must be greater than zero")
	}
	if len(req.Samples) == 0 || len(req.Samples) > 4096 {
		return fmt.Errorf("samples must contain between 1 and 4096 entries")
	}
	for i, sample := range req.Samples {
		name := strings.TrimSpace(sample.Name)
		if name == "" || len(name) > 128 {
			return fmt.Errorf("sample %d name must be 1-128 characters", i)
		}
		for _, ch := range name {
			if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '.' || ch == '_' || ch == '-') {
				return fmt.Errorf("sample %d name contains unsupported characters", i)
			}
		}
		if math.IsNaN(sample.Value) || math.IsInf(sample.Value, 0) {
			return fmt.Errorf("sample %d value must be finite", i)
		}
		if sample.Kind != "gauge" && sample.Kind != "counter" {
			return fmt.Errorf("sample %d kind must be gauge or counter", i)
		}
		if sample.Timestamp.IsZero() {
			return fmt.Errorf("sample %d timestamp is required", i)
		}
		if len(sample.Unit) > 32 || len(sample.Collector) > 64 {
			return fmt.Errorf("sample %d unit or collector is too long", i)
		}
		if len(sample.Attributes) > 8 {
			return fmt.Errorf("sample %d has too many attributes", i)
		}
		for k, v := range sample.Attributes {
			if strings.TrimSpace(k) == "" || len(k) > 64 || len(v) > 128 {
				return fmt.Errorf("sample %d has an invalid attribute", i)
			}
		}
	}
	return nil
}

func (s *Server) rotateToken(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		writeAPIError(w, 401, "admin_required", "admin token required")
		return
	}
	kind := strings.ToLower(strings.TrimSpace(r.PathValue("kind")))
	if kind != "join" && kind != "read" && kind != "admin" {
		writeAPIError(w, 400, "invalid_token_kind", "token kind must be join, read, or admin")
		return
	}
	tok, err := s.Store.RotateToken(kind)
	if err != nil {
		writeAPIError(w, 500, "storage_failure", "failed to rotate token")
		return
	}
	writeJSON(w, http.StatusOK, protocol.TokenRotationResponse{Kind: kind, Token: tok})
}

func validateHeartbeat(req protocol.HeartbeatRequest) error {
	percent := []struct {
		name string
		v    float64
	}{
		{"cpu_percent", req.Metrics.CPUPercent},
		{"ram_percent", req.Metrics.RAMPercent},
		{"disk_percent", req.Metrics.DiskPercent},
	}
	for _, x := range percent {
		if math.IsNaN(x.v) || math.IsInf(x.v, 0) || x.v < 0 || x.v > 100 {
			return fmt.Errorf("%s must be between 0 and 100", x.name)
		}
	}
	if req.Metrics.RAMTotalBytes > 0 && req.Metrics.RAMUsedBytes > req.Metrics.RAMTotalBytes {
		return fmt.Errorf("ram_used_bytes cannot exceed ram_total_bytes")
	}
	if req.Metrics.DiskTotalBytes > 0 && req.Metrics.DiskUsedBytes > req.Metrics.DiskTotalBytes {
		return fmt.Errorf("disk_used_bytes cannot exceed disk_total_bytes")
	}
	if req.Metrics.TemperatureC != nil {
		v := *req.Metrics.TemperatureC
		if math.IsNaN(v) || math.IsInf(v, 0) || v < -100 || v > 250 {
			return fmt.Errorf("temperature_c is outside the accepted range")
		}
	}
	seen := map[string]bool{}
	for _, svc := range req.Services {
		name := strings.TrimSpace(svc.Name)
		if name == "" || len(name) > 64 {
			return fmt.Errorf("service name must be 1-64 characters")
		}
		key := strings.ToLower(name)
		if seen[key] {
			return fmt.Errorf("duplicate service name %q", name)
		}
		seen[key] = true
		if svc.Type != "" && svc.Type != "tcp" && svc.Type != "http" && svc.Type != "systemd" {
			return fmt.Errorf("service %q has unsupported type", name)
		}
		if len(svc.Target) > 2048 {
			return fmt.Errorf("service %q target is too long", name)
		}
		if len(svc.Message) > 512 {
			return fmt.Errorf("service %q message is too long", name)
		}
		if svc.LatencyMS < 0 || svc.LatencyMS > 600000 {
			return fmt.Errorf("service %q latency is outside the accepted range", name)
		}
	}
	return nil
}

func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	if !s.readOK(r) {
		writeAPIError(w, 401, "unauthorized", "read or admin token required")
		return
	}
	writeJSON(w, 200, insight.Summary(s.Store.Views(time.Now().UTC()), s.Store.Alerts("")))
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	if !s.readOK(r) {
		writeAPIError(w, 401, "unauthorized", "read or admin token required")
		return
	}
	limit := 10
	if raw := strings.TrimSpace(r.URL.Query().Get("events")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 || v > 50 {
			writeAPIError(w, 400, "invalid_events_limit", "events must be between 0 and 50")
			return
		}
		limit = v
	}
	now := time.Now().UTC()
	events := []protocol.Event{}
	if limit > 0 {
		events = s.Store.Events(limit, "")
	}
	writeJSON(w, 200, insight.Overview(s.Store.Views(now), s.Store.Alerts(""), events, s.Store.Incidents(20, "", "open"), now))
}

func (s *Server) unhealthy(w http.ResponseWriter, r *http.Request) {
	if !s.readOK(r) {
		writeAPIError(w, 401, "unauthorized", "read or admin token required")
		return
	}
	views := s.Store.Views(time.Now().UTC())
	alerted := map[string]bool{}
	for _, a := range s.Store.Alerts("") {
		alerted[a.NodeID] = true
	}
	out := make([]protocol.NodeView, 0)
	for _, n := range views {
		bad := n.Status != "ONLINE" || alerted[n.ID]
		if !bad {
			for _, svc := range n.Services {
				if !svc.Healthy {
					bad = true
					break
				}
			}
		}
		if bad {
			out = append(out, n)
		}
	}
	writeJSON(w, 200, out)
}

func (s *Server) alerts(w http.ResponseWriter, r *http.Request) {
	if !s.readOK(r) {
		writeAPIError(w, 401, "unauthorized", "read or admin token required")
		return
	}
	writeJSON(w, http.StatusOK, s.Store.Alerts(strings.TrimSpace(r.URL.Query().Get("node"))))
}

func parseSince(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}
	v, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("since must be an RFC3339 timestamp")
	}
	return v.UTC(), nil
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	if !s.readOK(r) {
		writeAPIError(w, 401, "unauthorized", "read or admin token required")
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 500 {
			writeAPIError(w, 400, "invalid_limit", "limit must be between 1 and 500")
			return
		}
		limit = v
	}
	since, err := parseSince(r.URL.Query().Get("since"))
	if err != nil {
		writeAPIError(w, 400, "invalid_since", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.Store.EventsSince(limit, strings.TrimSpace(r.URL.Query().Get("node")), since))
}

func (s *Server) incidents(w http.ResponseWriter, r *http.Request) {
	if !s.readOK(r) {
		writeAPIError(w, 401, "unauthorized", "read or admin token required")
		return
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 || v > 500 {
			writeAPIError(w, 400, "invalid_limit", "limit must be between 1 and 500")
			return
		}
		limit = v
	}
	status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	if status != "" && status != "open" && status != "resolved" {
		writeAPIError(w, 400, "invalid_incident_status", "status must be open or resolved")
		return
	}
	since, err := parseSince(r.URL.Query().Get("since"))
	if err != nil {
		writeAPIError(w, 400, "invalid_since", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.Store.IncidentsSince(limit, strings.TrimSpace(r.URL.Query().Get("node")), status, since))
}

func (s *Server) incident(w http.ResponseWriter, r *http.Request) {
	if !s.readOK(r) {
		writeAPIError(w, 401, "unauthorized", "read or admin token required")
		return
	}
	detail, ok := s.Store.Incident(r.PathValue("id"))
	if !ok {
		writeAPIError(w, 404, "incident_not_found", "incident not found")
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (s *Server) nodes(w http.ResponseWriter, r *http.Request) {
	if !s.readOK(r) {
		writeAPIError(w, 401, "unauthorized", "read or admin token required")
		return
	}
	writeJSON(w, 200, s.Store.Views(time.Now().UTC()))
}

func (s *Server) node(w http.ResponseWriter, r *http.Request) {
	if !s.readOK(r) {
		writeAPIError(w, 401, "unauthorized", "read or admin token required")
		return
	}
	v, ok := s.Store.ViewByNameOrID(r.PathValue("id"), time.Now().UTC())
	if !ok {
		writeAPIError(w, 404, "node_not_found", "node not found")
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) services(w http.ResponseWriter, r *http.Request) {
	if !s.readOK(r) {
		writeAPIError(w, 401, "unauthorized", "read or admin token required")
		return
	}
	v, ok := s.Store.ViewByNameOrID(r.PathValue("id"), time.Now().UTC())
	if !ok {
		writeAPIError(w, 404, "node_not_found", "node not found")
		return
	}
	writeJSON(w, 200, v.Services)
}

func (s *Server) diagnosis(w http.ResponseWriter, r *http.Request) {
	if !s.readOK(r) {
		writeAPIError(w, 401, "unauthorized", "read or admin token required")
		return
	}
	now := time.Now().UTC()
	ref := r.PathValue("id")
	v, ok := s.Store.ViewByNameOrID(ref, now)
	if !ok {
		writeAPIError(w, 404, "node_not_found", "node not found")
		return
	}
	writeJSON(w, 200, insight.Diagnose(v, s.Store.Alerts(ref), now))
}

func (s *Server) renameNode(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		writeAPIError(w, 401, "admin_required", "admin token required")
		return
	}
	var req protocol.RenameNodeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeAPIError(w, 400, "invalid_json", "invalid JSON request")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if err := store.ValidateNodeName(req.Name); err != nil {
		writeAPIError(w, 400, "invalid_node_name", err.Error())
		return
	}
	if err := s.Store.RenameNode(r.PathValue("id"), req.Name); err != nil {
		switch {
		case errors.Is(err, os.ErrNotExist):
			writeAPIError(w, 404, "node_not_found", "node not found")
		case errors.Is(err, store.ErrNameExists):
			writeAPIError(w, 409, "node_name_exists", "node name already exists")
		default:
			writeAPIError(w, 500, "storage_failure", "storage failure")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteNode(w http.ResponseWriter, r *http.Request) {
	if !s.adminOK(r) {
		writeAPIError(w, 401, "admin_required", "admin token required")
		return
	}
	if err := s.Store.DeleteNode(r.PathValue("id")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeAPIError(w, 404, "node_not_found", "node not found")
		} else {
			writeAPIError(w, 500, "storage_failure", "storage failure")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) readOK(r *http.Request) bool {
	got := bearer(r.Header.Get("Authorization"))
	if got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.Store.ReadToken())) == 1 ||
		subtle.ConstantTimeCompare([]byte(got), []byte(s.Store.AdminToken())) == 1
}

func (s *Server) adminOK(r *http.Request) bool {
	got := bearer(r.Header.Get("Authorization"))
	want := s.Store.AdminToken()
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func bearer(v string) string {
	parts := strings.SplitN(v, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return strings.TrimSpace(parts[1])
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("request body must contain exactly one JSON value")
	}
	return nil
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, protocol.APIErrorBody{Error: protocol.APIError{Code: code, Message: message, RequestID: w.Header().Get("X-Request-ID")}})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}
