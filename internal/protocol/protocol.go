package protocol

import "time"

const (
	Version    = "1.2.0-dev"
	APIVersion = "v1"
)

type JoinRequest struct {
	// Token is retained for v0.2 compatibility. v0.3+ agents send the join
	// token in Authorization: Bearer instead.
	Token        string `json:"token,omitempty"`
	Name         string `json:"name"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	AgentVersion string `json:"agent_version"`
}

type JoinResponse struct {
	NodeID string `json:"node_id"`
	Secret string `json:"secret"`
}

type Metrics struct {
	CPUPercent     float64  `json:"cpu_percent"`
	RAMPercent     float64  `json:"ram_percent"`
	RAMUsedBytes   uint64   `json:"ram_used_bytes"`
	RAMTotalBytes  uint64   `json:"ram_total_bytes"`
	DiskPercent    float64  `json:"disk_percent"`
	DiskUsedBytes  uint64   `json:"disk_used_bytes"`
	DiskTotalBytes uint64   `json:"disk_total_bytes"`
	UptimeSeconds  uint64   `json:"uptime_seconds"`
	TemperatureC   *float64 `json:"temperature_c,omitempty"`
}

type ServiceSpec struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Target string `json:"target"`
}

type ServiceStatus struct {
	Name      string `json:"name"`
	Type      string `json:"type"`
	Target    string `json:"target"`
	Healthy   bool   `json:"healthy"`
	Message   string `json:"message,omitempty"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
}

type HeartbeatRequest struct {
	Metrics  Metrics         `json:"metrics"`
	Services []ServiceStatus `json:"services,omitempty"`
}


type TelemetrySample struct {
	Name       string            `json:"name"`
	Value      float64           `json:"value"`
	Unit       string            `json:"unit,omitempty"`
	Kind       string            `json:"kind"`
	Timestamp  time.Time         `json:"timestamp"`
	Attributes map[string]string `json:"attributes,omitempty"`
	Collector  string            `json:"collector,omitempty"`
}

type TelemetryBatch struct {
	Sequence uint64            `json:"sequence"`
	Samples  []TelemetrySample `json:"samples"`
}

type TelemetryAck struct {
	DurableSequence uint64 `json:"durable_sequence"`
	AcceptedSamples int    `json:"accepted_samples"`
}

type Node struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	OS            string          `json:"os"`
	Arch          string          `json:"arch"`
	AgentVersion  string          `json:"agent_version"`
	Secret        string          `json:"secret"`
	RegisteredAt  time.Time       `json:"registered_at"`
	LastHeartbeat time.Time       `json:"last_heartbeat"`
	Metrics       Metrics           `json:"metrics"`
	Services      []ServiceStatus   `json:"services,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	Groups        []string          `json:"groups,omitempty"`
}

type NodeView struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	OS            string          `json:"os"`
	Arch          string          `json:"arch"`
	AgentVersion  string          `json:"agent_version"`
	RegisteredAt  time.Time       `json:"registered_at"`
	LastHeartbeat time.Time       `json:"last_heartbeat"`
	Status        string          `json:"status"`
	Metrics       Metrics           `json:"metrics"`
	Services      []ServiceStatus   `json:"services,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	Groups        []string          `json:"groups,omitempty"`
}

type Alert struct {
	Key       string    `json:"key"`
	NodeID    string    `json:"node_id"`
	NodeName  string    `json:"node_name"`
	Kind      string    `json:"kind"`
	Severity  string    `json:"severity"`
	Subject   string    `json:"subject"`
	Message   string    `json:"message"`
	Since     time.Time `json:"since"`
	UpdatedAt time.Time `json:"updated_at"`
	Value     *float64  `json:"value,omitempty"`
	Threshold *float64  `json:"threshold,omitempty"`
}

type Event struct {
	ID         string    `json:"id"`
	OccurredAt time.Time `json:"occurred_at"`
	NodeID     string    `json:"node_id"`
	NodeName   string    `json:"node_name"`
	Kind       string    `json:"kind"`
	Severity   string    `json:"severity"`
	Subject    string    `json:"subject"`
	Message    string    `json:"message"`
}

type Incident struct {
	ID          string     `json:"id"`
	NodeID      string     `json:"node_id"`
	NodeName    string     `json:"node_name"`
	Status      string     `json:"status"`
	Severity    string     `json:"severity"`
	Title       string     `json:"title"`
	Summary     string     `json:"summary"`
	StartedAt   time.Time  `json:"started_at"`
	LastEventAt time.Time  `json:"last_event_at"`
	ResolvedAt  *time.Time `json:"resolved_at,omitempty"`
	DurationSec int64      `json:"duration_seconds"`
	EventCount  int        `json:"event_count"`
	EventIDs    []string   `json:"event_ids,omitempty"`
	Timeline    []Event    `json:"timeline,omitempty"`
	Kinds       []string   `json:"kinds,omitempty"`
	Subjects    []string   `json:"subjects,omitempty"`
}

type IncidentDetail struct {
	Incident Incident `json:"incident"`
	Events   []Event  `json:"events"`
}

type RenameNodeRequest struct {
	Name string `json:"name"`
}


type NodeMetadataRequest struct {
	Labels map[string]string `json:"labels,omitempty"`
	Groups []string          `json:"groups,omitempty"`
}

type APIInfo struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	APIVersion  string `json:"api_version"`
	StateSchema int    `json:"state_schema"`
}

type TokenRotationResponse struct {
	Kind  string `json:"kind"`
	Token string `json:"token"`
}

type Summary struct {
	Total             int `json:"total"`
	Online            int `json:"online"`
	Unstable          int `json:"unstable"`
	Offline           int `json:"offline"`
	HealthyServices   int `json:"healthy_services"`
	UnhealthyServices int `json:"unhealthy_services"`
	ActiveAlerts      int `json:"active_alerts"`
}

type APIErrorBody struct {
	Error APIError `json:"error"`
}

type APIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

type NodeDigest struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Status          string    `json:"status"`
	CPUPercent      float64   `json:"cpu_percent"`
	RAMPercent      float64   `json:"ram_percent"`
	DiskPercent     float64   `json:"disk_percent"`
	HealthyServices int       `json:"healthy_services"`
	TotalServices   int       `json:"total_services"`
	LastHeartbeat   time.Time `json:"last_heartbeat"`
	AgentVersion    string    `json:"agent_version"`
}

type Overview struct {
	GeneratedAt       time.Time    `json:"generated_at"`
	Status            string       `json:"status"`
	AttentionRequired bool         `json:"attention_required"`
	Headline          string       `json:"headline"`
	Summary           Summary      `json:"summary"`
	UnhealthyNodes    []NodeDigest `json:"unhealthy_nodes"`
	ActiveAlerts      []Alert      `json:"active_alerts"`
	ActiveIncidents   []Incident   `json:"active_incidents"`
	RecentEvents      []Event      `json:"recent_events"`
}

type Finding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
	Evidence string `json:"evidence,omitempty"`
}

type Diagnosis struct {
	GeneratedAt      time.Time `json:"generated_at"`
	Overall          string    `json:"overall"`
	Node             NodeView  `json:"node"`
	Findings         []Finding `json:"findings"`
	SuggestedActions []string  `json:"suggested_actions"`
}

type DoctorCheck struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message"`
}

type DoctorReport struct {
	ServerURL string        `json:"server_url"`
	Overall   string        `json:"overall"`
	Checks    []DoctorCheck `json:"checks"`
}


type SystemHealth struct {
	GeneratedAt            time.Time `json:"generated_at"`
	UptimeSeconds          uint64    `json:"uptime_seconds"`
	RequestsTotal          uint64    `json:"requests_total"`
	ClientErrorsTotal      uint64    `json:"client_errors_total"`
	ServerErrorsTotal      uint64    `json:"server_errors_total"`
	RequestAverageMS       float64   `json:"request_average_ms"`
	RequestMaxMS           float64   `json:"request_max_ms"`
	HeartbeatsTotal        uint64    `json:"heartbeats_total"`
	TelemetryBatchesTotal  uint64    `json:"telemetry_batches_total"`
	TelemetrySamplesTotal  uint64    `json:"telemetry_samples_total"`
	NodesTotal             int       `json:"nodes_total"`
	NodesOnline            int       `json:"nodes_online"`
	HistoryBytes           int64     `json:"history_bytes"`
	HistoryMaxBytes        int64     `json:"history_max_bytes"`
	HistoryRawSegments     int       `json:"history_raw_segments"`
	HistoryRollupFiles     int       `json:"history_rollup_files"`
	HistoryRollupBytes     int64     `json:"history_rollup_bytes"`
}
