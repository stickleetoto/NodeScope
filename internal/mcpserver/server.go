package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jjp-monitor/jjp/internal/apiclient"
	"github.com/jjp-monitor/jjp/internal/history"
	"github.com/jjp-monitor/jjp/internal/protocol"
)

const modernProtocol = "2026-07-28"
const legacyProtocol = "2025-11-25"

type Server struct {
	API        *apiclient.Client
	AllowWrite bool
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolDef struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	InputSchema  map[string]any `json:"inputSchema"`
	OutputSchema map[string]any `json:"outputSchema,omitempty"`
	Annotations  map[string]any `json:"annotations,omitempty"`
}

type callParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Meta      map[string]any  `json:"_meta,omitempty"`
}

func (s *Server) Run(ctx context.Context, in io.Reader, out io.Writer) error {
	if s.API == nil {
		return fmt.Errorf("MCP API client is required")
	}
	dec := json.NewDecoder(in)
	enc := json.NewEncoder(out)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var req rpcRequest
		if err := dec.Decode(&req); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("decode MCP request: %w", err)
		}
		if req.JSONRPC != "2.0" || strings.TrimSpace(req.Method) == "" {
			if hasID(req.ID) {
				_ = enc.Encode(errorResponse(req.ID, -32600, "Invalid Request"))
			}
			continue
		}
		resp, reply := s.handle(ctx, req)
		if reply {
			if err := enc.Encode(resp); err != nil {
				return err
			}
		}
	}
}

func (s *Server) handle(ctx context.Context, req rpcRequest) (rpcResponse, bool) {
	switch req.Method {
	case "notifications/initialized", "notifications/cancelled":
		return rpcResponse{}, false
	case "server/discover":
		return okResponse(req.ID, map[string]any{
			"resultType":        "complete",
			"supportedVersions": []string{modernProtocol, legacyProtocol},
			"capabilities":      map[string]any{"tools": map[string]any{"listChanged": false}},
			"instructions":      s.instructions(),
			"ttlMs":             300000,
			"cacheScope":        "private",
			"_meta":             serverMeta(),
		}), true
	case "initialize":
		version := legacyProtocol
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		for _, v := range []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"} {
			if p.ProtocolVersion == v {
				version = v
				break
			}
		}
		return okResponse(req.ID, map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "nodescope", "version": protocol.Version},
			"instructions":    s.instructions(),
		}), true
	case "ping":
		return okResponse(req.ID, map[string]any{}), true
	case "tools/list":
		result := map[string]any{"tools": s.tools()}
		if modern(req.Params) {
			result["resultType"] = "complete"
			result["ttlMs"] = 300000
			result["cacheScope"] = "private"
			result["_meta"] = serverMeta()
		}
		return okResponse(req.ID, result), true
	case "tools/call":
		var p callParams
		if err := json.Unmarshal(req.Params, &p); err != nil || strings.TrimSpace(p.Name) == "" {
			return errorResponse(req.ID, -32602, "Invalid params"), true
		}
		value, err := s.call(ctx, p.Name, p.Arguments)
		if err != nil {
			result := map[string]any{
				"content": []map[string]any{{"type": "text", "text": err.Error()}},
				"isError": true,
			}
			if modern(req.Params) {
				result["resultType"] = "complete"
				result["_meta"] = serverMeta()
			}
			return okResponse(req.ID, result), true
		}
		// Compact JSON is the backwards-compatible TextContent fallback. Modern
		// clients should prefer structuredContent and validate it against outputSchema.
		b, _ := json.Marshal(value)
		result := map[string]any{
			"content":           []map[string]any{{"type": "text", "text": string(b)}},
			"structuredContent": value,
			"isError":           false,
		}
		if modern(req.Params) {
			result["resultType"] = "complete"
			result["_meta"] = serverMeta()
		}
		return okResponse(req.ID, result), true
	default:
		if !hasID(req.ID) {
			return rpcResponse{}, false
		}
		return errorResponse(req.ID, -32601, "Method not found"), true
	}
}

func (s *Server) instructions() string {
	base := "Use get_overview first for broad questions about current status or what needs attention. For historical outage questions, use get_incidents first, then get_incident for the selected timeline; use get_recent_events only when raw event-level detail is necessary. Use diagnose_node only when one node needs deeper current-state explanation. Use get_node_trend for historical metric trends and get_metric_history only when raw metric evidence is needed. Prefer compact high-level tools over list_nodes to reduce unnecessary context. Incident correlation is deterministic by node and alert lifecycle; do not invent root causes that are not present in telemetry, findings, or event evidence."
	if s.AllowWrite {
		return base + " Rename/remove tools are enabled. Only mutate state when the user explicitly requests it. remove_node is destructive and requires confirm=true."
	}
	return base + " This MCP server is read-only."
}

func (s *Server) tools() []toolDef {
	empty := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	nodeArg := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"node": map[string]any{"type": "string", "description": "Node name or node ID"}},
		"required":   []string{"node"},
	}
	filterArg := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"node": map[string]any{"type": "string", "description": "Optional node name or node ID"}},
	}
	eventArg := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"node":          map[string]any{"type": "string", "description": "Optional node name or node ID"},
			"limit":         map[string]any{"type": "integer", "minimum": 1, "maximum": 500, "description": "Maximum number of newest events to return (default 50)"},
			"since_minutes": map[string]any{"type": "integer", "minimum": 1, "maximum": 525600, "description": "Optional lookback window in minutes (for example 1440 for the last 24 hours)"},
		},
	}
	incidentArg := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"node":          map[string]any{"type": "string", "description": "Optional node name or node ID"},
			"status":        map[string]any{"type": "string", "enum": []string{"open", "resolved"}, "description": "Optional incident status filter"},
			"limit":         map[string]any{"type": "integer", "minimum": 1, "maximum": 500, "description": "Maximum number of newest incidents to return (default 50)"},
			"since_minutes": map[string]any{"type": "integer", "minimum": 1, "maximum": 525600, "description": "Optional lookback window in minutes (for example 1440 for the last 24 hours)"},
		},
	}
	incidentIDArg := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"incident_id": map[string]any{"type": "string", "description": "Incident ID returned by get_incidents"}},
		"required":   []string{"incident_id"},
	}
	overviewArg := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{"events": map[string]any{"type": "integer", "minimum": 0, "maximum": 50, "description": "Recent events to include (default 10; use 0 to omit history)"}},
	}
	historyArg := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"node":          map[string]any{"type": "string", "description": "Node name or node ID"},
			"metric":        map[string]any{"type": "string", "description": "Exact metric name, for example system.memory.utilization"},
			"attributes":    map[string]any{"type": "object", "maxProperties": 8, "additionalProperties": map[string]any{"type": "string"}, "description": "Optional exact series filters such as interface=eth0, direction=receive, device=nvme0n1, or mount=/data"},
			"since_minutes": map[string]any{"type": "integer", "minimum": 1, "maximum": 525600, "description": "Lookback window in minutes (default 60)"},
			"limit":         map[string]any{"type": "integer", "minimum": 1, "maximum": 5000, "description": "Maximum number of newest metric points (default 500)"},
		},
		"required": []string{"node", "metric"},
	}
	readAnn := func(title string) map[string]any {
		return map[string]any{"title": title, "readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}
	}
	out := []toolDef{
		{Name: "get_overview", Description: "Preferred first tool. Get a compact AI-oriented snapshot with overall health, attention flag, counts, unhealthy-node digests, active alerts, and recent events.", InputSchema: overviewArg, OutputSchema: overviewSchema(), Annotations: readAnn("NodeScope Overview")},
		{Name: "diagnose_node", Description: "Explain one node using deterministic findings, evidence, severity, and suggested operator checks. Does not guess undocumented root causes.", InputSchema: nodeArg, OutputSchema: diagnosisSchema(), Annotations: readAnn("Diagnose NodeScope Node")},
		{Name: "get_summary", Description: "Get aggregate NodeScope node, service, and active-alert counts.", InputSchema: empty, OutputSchema: summarySchema(), Annotations: readAnn("NodeScope Summary")},
		{Name: "get_unhealthy_nodes", Description: "List nodes that are offline/unstable or have active health problems.", InputSchema: empty, OutputSchema: arraySchema(nodeSchema()), Annotations: readAnn("Unhealthy NodeScope Nodes")},
		{Name: "get_active_alerts", Description: "Get current unresolved NodeScope alerts, optionally filtered to one node.", InputSchema: filterArg, OutputSchema: arraySchema(alertSchema()), Annotations: readAnn("Active NodeScope Alerts")},
		{Name: "get_incidents", Description: "Preferred historical health tool. Get correlated node incidents instead of raw event logs, optionally filtered by node, open/resolved status, and a lookback window.", InputSchema: incidentArg, OutputSchema: arraySchema(incidentSchema()), Annotations: readAnn("NodeScope Incidents")},
		{Name: "get_incident", Description: "Get one incident with its ordered raw event timeline for evidence and chronology.", InputSchema: incidentIDArg, OutputSchema: incidentDetailSchema(), Annotations: readAnn("NodeScope Incident Timeline")},
		{Name: "get_recent_events", Description: "Get raw recent NodeScope health/recovery events, optionally limited to a lookback window. Prefer get_incidents for historical questions and use this only when event-level detail is needed.", InputSchema: eventArg, OutputSchema: arraySchema(eventSchema()), Annotations: readAnn("Recent NodeScope Events")},
		{Name: "get_node", Description: "Get detailed status for one NodeScope node by name or ID.", InputSchema: nodeArg, OutputSchema: nodeSchema(), Annotations: readAnn("NodeScope Node Details")},
		{Name: "list_services", Description: "Get monitored service states for one NodeScope node.", InputSchema: nodeArg, OutputSchema: arraySchema(serviceSchema()), Annotations: readAnn("NodeScope Node Services")},
		{Name: "list_nodes", Description: "List every registered NodeScope node with full metrics and service health. Prefer get_overview for broad health questions because this can return much more context.", InputSchema: empty, OutputSchema: arraySchema(nodeSchema()), Annotations: readAnn("All NodeScope Nodes")},
		{Name: "get_metric_history", Description: "Get bounded raw historical metric evidence for one node and exact metric name. Use a time window and prefer get_node_trend when aggregates are sufficient.", InputSchema: historyArg, OutputSchema: arraySchema(historyPointSchema()), Annotations: readAnn("NodeScope Metric History")},
		{Name: "get_node_trend", Description: "Summarize one node metric over a bounded lookback window using stored samples: count, min, max, average, first, last, delta, and per-hour rate.", InputSchema: historyArg, OutputSchema: trendSchema(), Annotations: readAnn("NodeScope Metric Trend")},
		{Name: "get_resource_peaks", Description: "Return deterministic min/max peak information for one node metric over a bounded lookback window.", InputSchema: historyArg, OutputSchema: trendSchema(), Annotations: readAnn("NodeScope Resource Peaks")},
	}
	if s.AllowWrite {
		out = append(out,
			toolDef{Name: "rename_node", Description: "Rename a registered NodeScope node. Use only after an explicit user request.", InputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"node":     map[string]any{"type": "string", "description": "Current node name or ID"},
					"new_name": map[string]any{"type": "string", "description": "New node name"},
				}, "required": []string{"node", "new_name"},
			}, OutputSchema: mutationSchema("renamed", "new_name"), Annotations: map[string]any{"title": "Rename NodeScope Node", "readOnlyHint": false, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}},
			toolDef{Name: "remove_node", Description: "Permanently remove a registered NodeScope node from central state and invalidate its agent credential. Requires confirm=true and an explicit user request.", InputSchema: map[string]any{
				"type": "object", "additionalProperties": false,
				"properties": map[string]any{
					"node":    map[string]any{"type": "string", "description": "Node name or node ID"},
					"confirm": map[string]any{"type": "boolean", "const": true, "description": "Must be true after the user explicitly requested deletion"},
				}, "required": []string{"node", "confirm"},
			}, OutputSchema: mutationSchema("removed", ""), Annotations: map[string]any{"title": "Remove NodeScope Node", "readOnlyHint": false, "destructiveHint": true, "idempotentHint": false, "openWorldHint": false}},
		)
	}
	return out
}

func (s *Server) call(ctx context.Context, name string, raw json.RawMessage) (any, error) {
	switch name {
	case "get_overview":
		events, err := optionalInt(raw, "events", 10, 0, 50)
		if err != nil {
			return nil, err
		}
		return s.API.Overview(ctx, events)
	case "diagnose_node":
		node, err := argString(raw, "node")
		if err != nil {
			return nil, err
		}
		return s.API.Diagnosis(ctx, node)
	case "get_summary":
		return s.API.Summary(ctx)
	case "list_nodes":
		return s.API.Nodes(ctx)
	case "get_unhealthy_nodes":
		return s.API.Unhealthy(ctx)
	case "get_active_alerts":
		return s.API.Alerts(ctx, optionalString(raw, "node"))
	case "get_incidents":
		limit, err := optionalInt(raw, "limit", 50, 1, 500)
		if err != nil {
			return nil, err
		}
		status := optionalString(raw, "status")
		if status != "" && status != "open" && status != "resolved" {
			return nil, fmt.Errorf("argument %q must be open or resolved", "status")
		}
		sinceMinutes, err := optionalInt(raw, "since_minutes", 0, 0, 525600)
		if err != nil {
			return nil, err
		}
		since := time.Time{}
		if sinceMinutes > 0 {
			since = time.Now().UTC().Add(-time.Duration(sinceMinutes) * time.Minute)
		}
		return s.API.IncidentsSince(ctx, limit, optionalString(raw, "node"), status, since)
	case "get_incident":
		id, err := argString(raw, "incident_id")
		if err != nil {
			return nil, err
		}
		return s.API.Incident(ctx, id)
	case "get_recent_events":
		limit, err := optionalInt(raw, "limit", 50, 1, 500)
		if err != nil {
			return nil, err
		}
		sinceMinutes, err := optionalInt(raw, "since_minutes", 0, 0, 525600)
		if err != nil {
			return nil, err
		}
		since := time.Time{}
		if sinceMinutes > 0 {
			since = time.Now().UTC().Add(-time.Duration(sinceMinutes) * time.Minute)
		}
		return s.API.EventsSince(ctx, limit, optionalString(raw, "node"), since)
	case "get_node":
		node, err := argString(raw, "node")
		if err != nil {
			return nil, err
		}
		return s.API.Node(ctx, node)
	case "list_services":
		node, err := argString(raw, "node")
		if err != nil {
			return nil, err
		}
		return s.API.Services(ctx, node)
	case "get_metric_history":
		node, err := argString(raw, "node")
		if err != nil {
			return nil, err
		}
		metric, err := argString(raw, "metric")
		if err != nil {
			return nil, err
		}
		sinceMinutes, err := optionalInt(raw, "since_minutes", 60, 1, 525600)
		if err != nil {
			return nil, err
		}
		limit, err := optionalInt(raw, "limit", 500, 1, 5000)
		if err != nil {
			return nil, err
		}
		attrs, err := optionalStringMap(raw, "attributes", 8)
		if err != nil {
			return nil, err
		}
		since := time.Now().UTC().Add(-time.Duration(sinceMinutes) * time.Minute)
		return s.API.MetricHistoryFiltered(ctx, node, metric, attrs, since, time.Time{}, limit)
	case "get_node_trend", "get_resource_peaks":
		node, err := argString(raw, "node")
		if err != nil {
			return nil, err
		}
		metric, err := argString(raw, "metric")
		if err != nil {
			return nil, err
		}
		sinceMinutes, err := optionalInt(raw, "since_minutes", 60, 1, 525600)
		if err != nil {
			return nil, err
		}
		attrs, err := optionalStringMap(raw, "attributes", 8)
		if err != nil {
			return nil, err
		}
		since := time.Now().UTC().Add(-time.Duration(sinceMinutes) * time.Minute)
		bucket := trendBucket(time.Duration(sinceMinutes) * time.Minute)
		buckets, err := s.API.MetricRollupFiltered(ctx, node, metric, attrs, since, time.Time{}, bucket)
		if err != nil {
			return nil, err
		}
		return summarizeRollupBuckets(node, metric, bucket, buckets), nil
	case "rename_node":
		if !s.AllowWrite {
			return nil, fmt.Errorf("write tools are disabled; start nodescope mcp with --allow-write")
		}
		node, err := argString(raw, "node")
		if err != nil {
			return nil, err
		}
		newName, err := argString(raw, "new_name")
		if err != nil {
			return nil, err
		}
		if err := s.API.Rename(ctx, node, newName); err != nil {
			return nil, err
		}
		return map[string]any{"renamed": true, "node": node, "new_name": newName}, nil
	case "remove_node":
		if !s.AllowWrite {
			return nil, fmt.Errorf("write tools are disabled; start nodescope mcp with --allow-write")
		}
		node, err := argString(raw, "node")
		if err != nil {
			return nil, err
		}
		confirm, err := argBool(raw, "confirm")
		if err != nil || !confirm {
			return nil, fmt.Errorf("remove_node requires confirm=true after explicit user approval")
		}
		if err := s.API.Remove(ctx, node); err != nil {
			return nil, err
		}
		return map[string]any{"removed": true, "node": node}, nil
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}

func argString(raw json.RawMessage, key string) (string, error) {
	var args map[string]any
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid tool arguments")
	}
	v, ok := args[key].(string)
	v = strings.TrimSpace(v)
	if !ok || v == "" {
		return "", fmt.Errorf("argument %q is required", key)
	}
	return v, nil
}

func argBool(raw json.RawMessage, key string) (bool, error) {
	var args map[string]any
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return false, fmt.Errorf("invalid tool arguments")
	}
	v, ok := args[key].(bool)
	if !ok {
		return false, fmt.Errorf("argument %q is required and must be boolean", key)
	}
	return v, nil
}

func optionalString(raw json.RawMessage, key string) string {
	if len(raw) == 0 {
		return ""
	}
	var args map[string]any
	if json.Unmarshal(raw, &args) != nil {
		return ""
	}
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

func optionalStringMap(raw json.RawMessage, key string, maxItems int) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("invalid tool arguments")
	}
	v, ok := args[key]
	if !ok {
		return nil, nil
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("argument %q must be an object", key)
	}
	if len(obj) > maxItems {
		return nil, fmt.Errorf("argument %q may contain at most %d entries", key, maxItems)
	}
	out := make(map[string]string, len(obj))
	for k, rawValue := range obj {
		value, ok := rawValue.(string)
		if !ok || strings.TrimSpace(k) == "" || len(k) > 64 || len(value) > 128 {
			return nil, fmt.Errorf("argument %q contains an invalid attribute", key)
		}
		out[k] = value
	}
	return out, nil
}

func optionalInt(raw json.RawMessage, key string, def, min, max int) (int, error) {
	if len(raw) == 0 {
		return def, nil
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil {
		return 0, fmt.Errorf("invalid tool arguments")
	}
	v, ok := args[key]
	if !ok {
		return def, nil
	}
	n, ok := v.(float64)
	if !ok || n != float64(int(n)) || int(n) < min || int(n) > max {
		return 0, fmt.Errorf("argument %q must be an integer between %d and %d", key, min, max)
	}
	return int(n), nil
}

func modern(raw json.RawMessage) bool {
	var p struct {
		Meta map[string]any `json:"_meta"`
	}
	if json.Unmarshal(raw, &p) != nil {
		return false
	}
	v, _ := p.Meta["io.modelcontextprotocol/protocolVersion"].(string)
	return v == modernProtocol
}

func serverMeta() map[string]any {
	return map[string]any{"io.modelcontextprotocol/serverInfo": map[string]any{"name": "nodescope", "version": protocol.Version}}
}

func hasID(id json.RawMessage) bool { return len(id) > 0 && string(id) != "null" }

func okResponse(id json.RawMessage, result any) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Result: result}
}

func errorResponse(id json.RawMessage, code int, message string) rpcResponse {
	return rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}
}

func trendBucket(window time.Duration) time.Duration {
	switch {
	case window <= 6*time.Hour:
		return time.Minute
	case window <= 7*24*time.Hour:
		return 5 * time.Minute
	default:
		return time.Hour
	}
}

func summarizeRollupBuckets(node, metric string, bucket time.Duration, buckets []history.RollupBucket) map[string]any {
	out := map[string]any{
		"node": node,
		"metric": metric,
		"bucket_seconds": bucket.Seconds(),
		"bucket_count": len(buckets),
	}
	if len(buckets) == 0 {
		out["count"] = 0
		return out
	}
	first := buckets[0]
	last := buckets[len(buckets)-1]
	minBucket, maxBucket := first, first
	totalCount := 0
	weightedSum := 0.0
	for _, b := range buckets {
		totalCount += b.Count
		weightedSum += b.Average * float64(b.Count)
		if b.Min < minBucket.Min {
			minBucket = b
		}
		if b.Max > maxBucket.Max {
			maxBucket = b
		}
	}
	out["count"] = totalCount
	out["kind"] = first.Kind
	out["unit"] = first.Unit
	out["first"] = map[string]any{"value": first.First, "timestamp": first.Start}
	out["last"] = map[string]any{"value": last.Last, "timestamp": last.End}
	out["min"] = map[string]any{"value": minBucket.Min, "timestamp": minBucket.Start}
	out["max"] = map[string]any{"value": maxBucket.Max, "timestamp": maxBucket.Start}
	if totalCount > 0 {
		out["average"] = weightedSum / float64(totalCount)
	}
	out["delta"] = last.Last - first.First
	duration := last.End.Sub(first.Start)
	out["duration_seconds"] = duration.Seconds()
	if duration > 0 {
		out["rate_per_hour"] = (last.Last - first.First) / duration.Hours()
	}
	return out
}

func summarizeMetricPoints(node, metric string, points []history.Point) map[string]any {
	out := map[string]any{
		"node": node,
		"metric": metric,
		"count": len(points),
	}
	if len(points) == 0 {
		return out
	}
	first := points[0].Sample
	last := points[len(points)-1].Sample
	minPoint, maxPoint := points[0], points[0]
	sum := 0.0
	for _, p := range points {
		sum += p.Sample.Value
		if p.Sample.Value < minPoint.Sample.Value {
			minPoint = p
		}
		if p.Sample.Value > maxPoint.Sample.Value {
			maxPoint = p
		}
	}
	out["kind"] = first.Kind
	out["unit"] = first.Unit
	out["first"] = map[string]any{"value": first.Value, "timestamp": first.Timestamp}
	out["last"] = map[string]any{"value": last.Value, "timestamp": last.Timestamp}
	out["min"] = map[string]any{"value": minPoint.Sample.Value, "timestamp": minPoint.Sample.Timestamp}
	out["max"] = map[string]any{"value": maxPoint.Sample.Value, "timestamp": maxPoint.Sample.Timestamp}
	out["average"] = sum / float64(len(points))
	out["delta"] = last.Value - first.Value
	duration := last.Timestamp.Sub(first.Timestamp)
	out["duration_seconds"] = duration.Seconds()
	if duration > 0 {
		out["rate_per_hour"] = (last.Value - first.Value) / duration.Hours()
	}
	return out
}

func historyPointSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"node_id": map[string]any{"type": "string"},
			"sequence": map[string]any{"type": "integer"},
			"sample": map[string]any{"type": "object"},
		},
		"required": []string{"node_id", "sequence", "sample"},
	}
}

func trendSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"node": map[string]any{"type": "string"},
			"metric": map[string]any{"type": "string"},
			"count": map[string]any{"type": "integer"},
			"kind": map[string]any{"type": "string"},
			"unit": map[string]any{"type": "string"},
			"first": map[string]any{"type": "object"},
			"last": map[string]any{"type": "object"},
			"min": map[string]any{"type": "object"},
			"max": map[string]any{"type": "object"},
			"average": map[string]any{"type": "number"},
			"delta": map[string]any{"type": "number"},
			"duration_seconds": map[string]any{"type": "number"},
			"rate_per_hour": map[string]any{"type": "number"},
		},
		"required": []string{"node", "metric", "count"},
	}
}

func summarySchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"total": map[string]any{"type": "integer"}, "online": map[string]any{"type": "integer"}, "unstable": map[string]any{"type": "integer"}, "offline": map[string]any{"type": "integer"},
		"healthy_services": map[string]any{"type": "integer"}, "unhealthy_services": map[string]any{"type": "integer"}, "active_alerts": map[string]any{"type": "integer"},
	}, "required": []string{"total", "online", "unstable", "offline", "healthy_services", "unhealthy_services", "active_alerts"}}
}

func nodeSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"id": map[string]any{"type": "string"}, "name": map[string]any{"type": "string"}, "status": map[string]any{"type": "string", "enum": []string{"ONLINE", "UNSTABLE", "OFFLINE"}},
		"agent_version": map[string]any{"type": "string"}, "last_heartbeat": map[string]any{"type": "string"}, "metrics": map[string]any{"type": "object"}, "services": map[string]any{"type": "array"},
	}, "required": []string{"id", "name", "status", "metrics"}}
}

func serviceSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"name": map[string]any{"type": "string"}, "type": map[string]any{"type": "string"}, "target": map[string]any{"type": "string"}, "healthy": map[string]any{"type": "boolean"}, "message": map[string]any{"type": "string"}, "latency_ms": map[string]any{"type": "integer"},
	}, "required": []string{"name", "healthy"}}
}

func alertSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"key": map[string]any{"type": "string"}, "node_id": map[string]any{"type": "string"}, "node_name": map[string]any{"type": "string"}, "kind": map[string]any{"type": "string"}, "severity": map[string]any{"type": "string"}, "subject": map[string]any{"type": "string"}, "message": map[string]any{"type": "string"}, "since": map[string]any{"type": "string"},
	}, "required": []string{"key", "node_id", "node_name", "kind", "severity", "message", "since"}}
}

func eventSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"id": map[string]any{"type": "string"}, "occurred_at": map[string]any{"type": "string"}, "node_id": map[string]any{"type": "string"}, "node_name": map[string]any{"type": "string"}, "kind": map[string]any{"type": "string"}, "severity": map[string]any{"type": "string"}, "subject": map[string]any{"type": "string"}, "message": map[string]any{"type": "string"},
	}, "required": []string{"id", "occurred_at", "node_id", "node_name", "kind", "severity", "message"}}
}

func incidentSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"id": map[string]any{"type": "string"}, "node_id": map[string]any{"type": "string"}, "node_name": map[string]any{"type": "string"},
		"status": map[string]any{"type": "string", "enum": []string{"open", "resolved"}}, "severity": map[string]any{"type": "string"},
		"title": map[string]any{"type": "string"}, "summary": map[string]any{"type": "string"}, "started_at": map[string]any{"type": "string"},
		"last_event_at": map[string]any{"type": "string"}, "resolved_at": map[string]any{"type": "string"}, "duration_seconds": map[string]any{"type": "integer"},
		"event_count": map[string]any{"type": "integer"}, "event_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"kinds": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "subjects": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	}, "required": []string{"id", "node_id", "node_name", "status", "severity", "title", "summary", "started_at", "last_event_at", "duration_seconds", "event_count"}}
}

func incidentDetailSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"incident": incidentSchema(), "events": arraySchema(eventSchema()),
	}, "required": []string{"incident", "events"}}
}

func overviewSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"generated_at": map[string]any{"type": "string"}, "status": map[string]any{"type": "string", "enum": []string{"healthy", "degraded", "critical"}}, "attention_required": map[string]any{"type": "boolean"}, "headline": map[string]any{"type": "string"},
		"summary": summarySchema(), "unhealthy_nodes": map[string]any{"type": "array", "items": map[string]any{"type": "object"}}, "active_alerts": arraySchema(alertSchema()), "active_incidents": arraySchema(incidentSchema()), "recent_events": arraySchema(eventSchema()),
	}, "required": []string{"generated_at", "status", "attention_required", "headline", "summary", "unhealthy_nodes", "active_alerts", "active_incidents", "recent_events"}}
}

func diagnosisSchema() map[string]any {
	return map[string]any{"type": "object", "properties": map[string]any{
		"generated_at": map[string]any{"type": "string"}, "overall": map[string]any{"type": "string", "enum": []string{"healthy", "warning", "critical"}}, "node": nodeSchema(),
		"findings":          map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"code": map[string]any{"type": "string"}, "severity": map[string]any{"type": "string"}, "summary": map[string]any{"type": "string"}, "evidence": map[string]any{"type": "string"}}, "required": []string{"code", "severity", "summary"}}},
		"suggested_actions": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	}, "required": []string{"generated_at", "overall", "node", "findings", "suggested_actions"}}
}

func arraySchema(item map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": item}
}

func mutationSchema(flag, extra string) map[string]any {
	props := map[string]any{flag: map[string]any{"type": "boolean"}, "node": map[string]any{"type": "string"}}
	required := []string{flag, "node"}
	if extra != "" {
		props[extra] = map[string]any{"type": "string"}
		required = append(required, extra)
	}
	return map[string]any{"type": "object", "properties": props, "required": required}
}
