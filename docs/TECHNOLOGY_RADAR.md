# NodeScope Technology Radar

This document tracks technologies that can extend NodeScope without losing its core properties: **lightweight, local-first, operator-friendly, AI-readable, and safe by default**.

## 1. Current foundation

NodeScope v1.1 already has a strong control/observability baseline:

- outbound agent heartbeats
- CPU / RAM / disk / uptime / temperature metrics
- TCP / HTTP / systemd service checks
- ONLINE / UNSTABLE / OFFLINE node state
- persistent alerts, events, and correlated incidents
- deterministic diagnosis and suggested operator actions
- HTTP API v1
- CLI with JSON output
- MCP read tools plus explicitly-gated admin tools
- token separation: join / read / admin / per-node secret
- versioned state schema, atomic replacement, exclusive state lock
- live backup / guarded restore
- Linux amd64/arm64 and Windows amd64 builds
- test, race, vet, and cross-build CI

The next phase should deepen telemetry and interoperability rather than immediately adding arbitrary remote control.

---

## 2. Technology tracks

### A. Telemetry History / Local Time-Series

**Goal:** answer questions such as:

- "Has RAM been rising for six hours?"
- "When did disk latency start increasing?"
- "Which node had the largest CPU peak today?"
- "Was the service outage preceded by memory pressure?"

Current NodeScope mostly stores the latest metric snapshot plus problem events. Add a history plane separate from control-plane state.

Recommended architecture:

```text
heartbeat
   |
   +--> latest state (existing JSON control state)
   |
   +--> history ingest
          |
          +--> short raw window
          +--> 1m rollup
          +--> 5m rollup
          +--> 1h rollup
```

Initial retention target:

| Resolution | Retention | Purpose |
|---|---:|---|
| raw / heartbeat | 1-6 h | incident forensics |
| 1 minute | 24 h | current-day trends |
| 5 minutes | 7 d | weekly comparisons |
| 1 hour | 30-90 d | capacity planning |

Do **not** turn NodeScope into a full Prometheus replacement. A compact append-only segment format with bounded retention is enough for local trend analysis.

Potential record model:

```go
type MetricPoint struct {
    Timestamp int64
    NodeID    string
    MetricID  uint16
    Value     float64
}
```

Keep high-cardinality labels out of the core history format. Add only bounded dimensions such as disk, interface, CPU mode, or service name where explicitly supported.

### B. Collector v2

Replace the single monolithic metric collection path with independent collectors:

```go
type Collector interface {
    Name() string
    Collect(context.Context) ([]Sample, error)
}
```

Recommended core collectors:

- CPU total + user/system/iowait where available
- memory usage / available / swap
- filesystem usage per selected mount
- disk I/O bytes, ops, queue time / latency where available
- network RX/TX bytes, errors, drops per selected interface
- load average on Unix-like systems
- process counts
- temperature / hwmon
- Linux PSI: CPU / memory / I/O pressure
- NodeScope agent self-metrics

Use an internal semantic namespace that can map cleanly to OpenTelemetry `system.*`, `process.*`, and `hw.*` conventions, while avoiding making unstable external semantic conventions part of NodeScope's permanent ABI.

Example internal names:

```text
system.cpu.utilization
system.memory.usage
system.filesystem.usage
system.network.io
system.network.errors
system.process.count
system.pressure.cpu
system.pressure.memory
system.pressure.io
nodescope.agent.queue.depth
nodescope.agent.send.failures
```

### C. Pressure-aware Linux monitoring

Linux PSI exposes real CPU, memory, and I/O contention through `/proc/pressure/*`.

This is more useful than percentage thresholds alone because it measures time workloads are actually stalled.

Use PSI in two levels:

1. sampled PSI averages in normal heartbeats
2. optional PSI trigger mode for fast pressure alerts

This can distinguish:

```text
"CPU is busy"
from
"work is actually stalled waiting for CPU"
```

### D. Agent WAL / Store-and-Forward

Current retry/backoff protects connectivity but does not preserve telemetry from a central outage.

Add a bounded local WAL:

```text
collect
  |
  v
bounded WAL --> send batch --> ACK --> truncate/checkpoint
                    |
                    +-- failure --> retry with backoff
```

Design targets:

- crash-safe append-only records
- checksummed frames
- bounded by size and/or age
- segment rotation
- replay after reconnect
- explicit drop counters when capacity is exceeded
- no unbounded disk growth
- sequence number per agent
- server-side duplicate suppression / idempotent replay

Suggested initial defaults:

- 16 MiB local queue
- 2-6 hour age limit
- batch by size or short timeout
- exponential retry with jitter

Expose self-observability metrics:

```text
queue_depth
queue_capacity
records_dropped
send_failures
replay_records
oldest_record_age
last_successful_send
```

### E. Batching and backpressure

Borrow production patterns from telemetry collectors:

- queue before network export
- batch by size and timeout
- hard queue capacity
- retry transient failures
- distinguish enqueue failure from send failure
- shed load rather than exhaust agent memory

NodeScope should prefer bounded degradation over process instability.

### F. Prometheus compatibility

First interoperability step:

```text
GET /metrics
```

Expose the NodeScope central server's current aggregate state and optionally latest node metrics.

Examples:

```text
nodescope_nodes_total
nodescope_nodes_online
nodescope_active_alerts
nodescope_active_incidents
nodescope_node_cpu_ratio{node_id="..."}
nodescope_node_memory_ratio{node_id="..."}
nodescope_service_up{node_id="...",service="..."}
```

Avoid unbounded labels such as arbitrary user strings in metric dimensions.

Prefer Prometheus exposition before implementing Remote Write. Remote Write 2.0 is useful but should stay optional until NodeScope has a clear remote-storage use case.

### G. OTLP export

OTLP is the preferred standard export path for NodeScope telemetry.

Recommended initial scope:

- OTLP/HTTP metrics exporter
- optional gzip
- configurable endpoint and headers
- bounded sending queue
- retry with backoff
- mapping of NodeScope resource attributes to host identity

Later:

- NodeScope incidents/events -> OTLP logs/events where useful
- traces for NodeScope's own agent/server HTTP operations
- profiles only as an experimental future module

OTLP should be an **exporter**, not NodeScope's internal data model.

### H. Node identity, labels, and groups

Add bounded metadata:

```text
node ID
display name
labels:
  role=storage
  site=home
  arch=arm64
groups:
  raspberry-pi
  gpu-machines
  servers
```

Use cases:

```text
nodescope ls --group servers
nodescope overview --label role=storage
compare_nodes(group="raspberry-pi")
```

Rules:

- labels must have cardinality limits
- labels are operator metadata, not arbitrary telemetry dimensions
- IDs remain immutable even when display names change

### I. Alert routing

Add outbound notification sinks without turning NodeScope into a full incident-management product.

Initial sinks:

- generic webhook
- Discord-compatible webhook
- optional email later

Features:

- severity filter
- node/group filter
- cooldown / deduplication
- recovery notification
- signed webhook payloads
- retry queue

### J. Remote MCP transport

NodeScope already has an AI-first MCP tool model.

Add a stateless HTTP MCP adapter:

```text
POST /mcp
```

Design around the MCP 2026-07-28 stateless core:

- requests independently routable
- protocol version in request metadata / headers
- `server/discover`
- cacheable tool list
- header-aware routing
- strict authorization
- read-only by default

Preserve stdio MCP for local workflows.

### K. Fleet management inspired by OpAMP

Do not immediately implement full OpAMP.

Adopt the useful concepts first:

```text
AgentCapabilities
AgentVersion
ConfigVersion
ConfigHash
EffectiveConfig
DesiredConfig
AgentStatus
UpdateStatus
```

This prepares NodeScope v2 for controlled management while keeping v1 observation-only.

Potential v2 typed actions:

- update NodeScope agent
- apply collector configuration
- restart the NodeScope agent
- rotate node credentials
- enable/disable a collector
- request a diagnostic bundle

Avoid arbitrary shell as a default management primitive.

### L. mTLS and credential lifecycle

Future security track:

- per-node client certificates
- CA pinning / trust bundle
- certificate rotation
- revocation
- bootstrap token only for enrollment
- short-lived or renewable credentials
- audit events for all credential changes

Keep token mode for simple home/lab deployments.

### M. Cardinality controls

As collectors, labels, filesystems, interfaces, and processes are added, cardinality becomes a first-class resource limit.

Required controls:

- max metrics per heartbeat
- max interfaces / filesystems / services
- label count + key/value length limits
- allow/deny filters
- top-N process mode rather than every process by default
- per-node telemetry byte budget

Add diagnostics:

```text
telemetry_bytes_per_minute
samples_per_heartbeat
dropped_dimensions
collector_duration
collector_errors
```

### N. NodeScope self-observability

NodeScope should observe itself.

Agent:

- process RSS
- CPU time
- collector duration
- heartbeat payload bytes
- queue depth
- retry count
- dropped records

Central:

- request rate
- request latency
- heartbeat ingest rate
- state save latency
- history ingest latency
- active connections
- history disk bytes
- WAL replay / duplicate count
- MCP calls and errors

This makes performance regressions visible without external profilers.

### O. eBPF observability — experimental

Use only as an optional Linux module after the normal collector path is mature.

Possible use cases:

- TCP connection latency
- retransmits / failed connects
- process exec/exit events
- network flow summaries
- scheduler latency
- block I/O latency
- low-overhead kernel event streams

Do not make eBPF mandatory:

- requires newer Linux features and permissions
- increases complexity
- has kernel/version compatibility costs

A Go implementation can use the Cilium eBPF ecosystem later.

### P. Continuous profiling — future experiment

OpenTelemetry Profiles is still an early-maturity signal.

Potential long-term NodeScope use:

- CPU profile during an incident
- allocator / memory profile
- on-demand profile capture with explicit operator approval

Treat this as a v2.x experimental feature, not a v1 requirement.

---

## 3. Standards adoption policy

| Technology | Current maturity | NodeScope decision |
|---|---|---|
| OTLP traces/metrics/logs | Stable | Adopt for export |
| OTel system semantic conventions | Development / mixed | Map to them, do not freeze them into core ABI |
| OpAMP | Beta | Borrow architecture now, optional compatibility later |
| Prometheus exposition | Mature ecosystem | Add early |
| Prometheus Remote Write 1.x | Published | Optional compatibility if needed |
| Prometheus Remote Write 2.0 | Experimental | Watch / optional later |
| MCP 2026-07-28 | Current MCP spec | Extend remote stateless transport |
| Linux PSI | Kernel interface | Adopt in Linux collector |
| eBPF | Mature kernel technology, complex integration | Experimental optional collector |
| OTel Profiles | Public Alpha | Watch / experiment later |

---

## 4. Architectural boundary

Keep these planes separate:

```text
                  +--------------------+
                  |  Control Plane     |
                  | nodes / auth / cfg |
                  +--------------------+
                            |
Agent --> Ingest --> Current State
  |                         |
  |                         +--> Alerts --> Incidents --> Diagnosis
  |
  +--> Local WAL
                            |
                            +--> History Plane --> Rollups / Trends
                            |
                            +--> Export Plane
                                  |- Prometheus
                                  |- OTLP
                                  `- MCP/API
```

Do not store high-rate historical telemetry in the control-state JSON.

---

## 5. Non-goals

NodeScope should not become:

- a full Prometheus TSDB
- a Grafana replacement
- a generic SSH/RMM shell
- a Kubernetes replacement
- an unrestricted AI remote-control daemon
- a high-cardinality process telemetry database by default

The product advantage is **compact fleet observability + deterministic diagnosis + AI-native access**.

---

## 6. Reference technologies

Primary references used for this radar:

- OpenTelemetry OTLP specification: https://opentelemetry.io/docs/specs/otlp/
- OpenTelemetry system semantic conventions: https://opentelemetry.io/docs/specs/semconv/system/system-metrics/
- OpenTelemetry OpAMP: https://opentelemetry.io/docs/specs/opamp/
- OpenTelemetry Collector internal telemetry/scaling guidance:
  - https://opentelemetry.io/docs/collector/internal-telemetry/
  - https://opentelemetry.io/docs/collector/scaling/
- Prometheus storage/WAL: https://prometheus.io/docs/prometheus/latest/storage/
- Prometheus Remote Write:
  - https://prometheus.io/docs/specs/prw/remote_write_spec/
  - https://prometheus.io/docs/specs/prw/remote_write_spec_2_0/
- Linux PSI: https://docs.kernel.org/accounting/psi.html
- eBPF docs: https://docs.ebpf.io/
- MCP 2026-07-28 overview: https://blog.modelcontextprotocol.io/posts/2026-07-28/
- OpenTelemetry Profiles alpha: https://opentelemetry.io/blog/2026/profiles-alpha/
