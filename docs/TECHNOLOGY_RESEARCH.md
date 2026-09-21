# NodeScope Technology Research

This document records technologies, design patterns, and standards that can strengthen NodeScope without turning it into a heavyweight monitoring stack.

## Product constraints

NodeScope should remain:

- lightweight and self-hostable;
- CLI/API/MCP first;
- useful on Raspberry Pi, ordinary Linux servers, and Windows machines;
- safe by default for AI access;
- compatible with a single-binary deployment model;
- understandable without requiring Prometheus, Grafana, Kubernetes, or a cloud service;
- capable of integrating with those ecosystems when users already have them.

The core principle is: **build a compact observability control plane, not a smaller clone of every observability product.**

---

## Current baseline

NodeScope already has:

- outbound agent heartbeats;
- CPU, RAM, disk, uptime, and Linux temperature telemetry;
- TCP, HTTP, and systemd service checks;
- ONLINE / UNSTABLE / OFFLINE node state;
- persistent alerts, events, and incident correlation;
- deterministic diagnosis and suggested actions;
- state schema migration, validation, locking, backup, and restore;
- read/admin credential separation and per-node credentials;
- HTTP API v1;
- structured MCP tools with read-only defaults;
- Linux amd64/arm64 and Windows amd64 builds;
- race/test/vet/build CI.

The biggest missing layer is **historical telemetry**. NodeScope currently keeps the newest metric snapshot plus events generated when something becomes unhealthy. That is enough for current-state diagnosis, but not for trend, capacity, or anomaly questions.

---

# Technology directions

## 1. Metric model v2

Move from one fixed Metrics struct toward a typed metric model.

Proposed internal shape:

```go
type Sample struct {
    Name       string
    Value      float64
    Unit       string
    Kind       MetricKind
    Timestamp  time.Time
    Attributes map[string]string
}
```

The protocol does not need to expose this exact Go type, but the storage and collector layers should understand:

- gauges;
- monotonic counters;
- units;
- timestamps;
- dimensions/attributes;
- metric stability;
- collector origin.

NodeScope metric names should be inspired by OpenTelemetry system semantic conventions where practical, without promising exact OpenTelemetry compatibility while those conventions remain under development.

Examples:

```text
system.cpu.utilization
system.memory.utilization
system.memory.usage
system.paging.utilization
system.disk.io
system.network.io
system.process.count
system.uptime
```

Benefits:

- collector expansion without growing one giant protocol struct forever;
- easier Prometheus/OpenMetrics and OTLP export later;
- cleaner history queries;
- less migration pain when adding new metrics.

Guardrail: keep a small stable NodeScope core namespace and map to external conventions at exporter boundaries when the external convention is unstable.

---

## 2. Collector v2

Replace monolithic host collection with independent collectors.

Suggested interface:

```go
type Collector interface {
    Name() string
    Collect(context.Context) ([]Sample, error)
}
```

Initial collectors:

### Cross-platform

- CPU utilization
- memory usage/utilization
- filesystem capacity
- uptime
- process count

### Linux

- load average from /proc/loadavg
- per-interface network bytes/packets/errors
- disk read/write bytes and operations from /proc/diskstats
- swap/paging
- filesystem-per-mount usage
- hwmon/thermal temperature
- Linux PSI: CPU, memory, and I/O pressure
- process-state counts

### Windows

- CPU
- memory
- logical disks beyond only C:
- network interfaces
- pagefile pressure
- process count
- service state
- optional GPU metrics when a reliable implementation path exists

Collectors should advertise capability and availability so the central server does not interpret unsupported metrics as zero.

---

## 3. Linux PSI as a first-class signal

Pressure Stall Information reports how much useful work is delayed by CPU, memory, or I/O contention.

This is more informative than utilization alone.

Example:

```text
CPU 95% + PSI near 0      -> busy, but work is progressing
CPU 80% + high CPU PSI    -> tasks are actually waiting for CPU
RAM 75% + high memory PSI -> memory pressure despite apparently safe percentage
I/O PSI high              -> storage contention is delaying work
```

NodeScope should collect:

- avg10 / avg60 / avg300;
- total stall time;
- "some" pressure;
- "full" pressure where supported.

Potential alerts should use sustained PSI rather than single-sample spikes.

---

## 4. Historical metric store

Do not build full Prometheus TSDB or PromQL.

Build a NodeScope-specific bounded history store optimized for host telemetry and AI queries.

Recommended retention tiers:

```text
raw / 10-30 s samples     -> 2-6 hours
1 minute rollups          -> 24-48 hours
5 minute rollups          -> 7 days
1 hour rollups            -> 30-90 days
```

Each rollup bucket should retain:

- min;
- max;
- average;
- last;
- count.

This is enough for most operator and AI questions while staying compact.

Suggested API concepts:

```text
GET /api/v1/metrics/history
GET /api/v1/nodes/{id}/metrics/history
GET /api/v1/nodes/{id}/metrics/trend
```

Suggested MCP tools:

```text
get_metric_history
get_node_trend
compare_nodes
get_resource_peaks
get_capacity_risk
```

Cardinality controls are mandatory:

- bounded metric names;
- bounded attribute keys;
- bounded attribute values;
- no arbitrary user-provided unbounded labels on every sample.

---

## 5. Append-only history segments

Keep configuration/state JSON separate from metric history.

Suggested layout:

```text
state/
  server.json

metrics/
  head.wal
  segments/
    2026-09-22T00.seg
    2026-09-22T01.seg
  rollup/
    1m/
    5m/
    1h/
```

Design goals:

- append sequentially;
- recover after crash;
- periodic segment sealing;
- checksum records;
- truncate incomplete tail safely;
- compact sealed raw segments into rollups;
- deterministic retention deletion.

Prometheus uses a WAL to protect recent in-memory samples before they are compacted into persistent blocks. NodeScope can adopt the reliability concept without adopting Prometheus's TSDB design.

---

## 6. Agent offline WAL / store-and-forward

Today, failed heartbeats are retried, but telemetry collected while the central server is unavailable is not preserved as a durable queue.

Add a small agent-side WAL:

```text
collect
   |
   v
bounded local WAL
   |
   +---- network unavailable ----> keep
   |
   +---- central ACK ------------> advance/delete
```

Recommended properties:

- sequence number per agent;
- batch IDs;
- idempotency key;
- server ACK of highest durable sequence;
- bounded by time and bytes;
- oldest-first eviction only after configured limit;
- visible dropped-sample counter;
- WAL health shown in doctor.

Suggested defaults:

- 4 hour max age;
- 16-64 MiB max size;
- compressed or compact binary records only if measurement proves worthwhile.

This is conceptually similar to durable WAL-backed telemetry queues used by established monitoring systems, but should remain much smaller and simpler.

---

## 7. Batching and adaptive heartbeat payloads

Separate liveness heartbeat from telemetry shipment.

Possible model:

```text
heartbeat: tiny liveness/status message
telemetry batch: multiple samples, less frequent or adaptive
```

Benefits:

- thousands of agents do not need to send a full JSON payload every heartbeat;
- server can negotiate intervals;
- future remote configuration becomes easier;
- agent can preserve liveness while collectors are slow.

Possible adaptive policy:

- healthy/idle node -> slower full telemetry;
- unhealthy/high-pressure node -> temporarily higher-resolution telemetry;
- heartbeat always stays cheap.

This should remain deterministic and bounded.

---

## 8. Node labels and groups

Add first-class metadata:

```text
role=database
site=home
arch=arm64
environment=lab
owner=devseat
group=edge
```

Use cases:

- filter fleet;
- compare equivalent machines;
- apply alert policy by group;
- maintenance windows;
- future controlled configuration;
- Prometheus labels/export;
- AI questions such as "show unhealthy edge nodes".

Important: label count and size must be bounded to avoid uncontrolled cardinality.

---

## 9. Alert engine v2

Current alerting already supports sustained thresholds. Expand it carefully.

Add:

- recovery thresholds / hysteresis;
- per-node or per-group policy overrides;
- maintenance windows;
- alert silences with expiry;
- notification cooldown;
- repeated-failure counters;
- alert fingerprints;
- dependency-aware suppression later.

Example hysteresis:

```text
CPU alert opens >= 90% for 5m
CPU alert resolves < 80% for 2m
```

This prevents threshold flapping.

Keep incident correlation deterministic and evidence-based.

---

## 10. Prometheus / OpenMetrics export

Add a read-only endpoint:

```text
GET /metrics
```

Expose NodeScope's current fleet state rather than turning NodeScope into a Prometheus replacement.

Examples:

```text
nodescope_node_up{node="pi-main"} 1
nodescope_node_cpu_ratio{node="pi-main"} 0.24
nodescope_node_memory_ratio{node="pi-main"} 0.61
nodescope_service_up{node="pi-main",service="api"} 1
nodescope_active_incidents 2
nodescope_agent_wal_bytes{node="pi-main"} 4096
```

Start with Prometheus text/OpenMetrics exposition.

Do not make Prometheus Remote Write 2.0 a core dependency yet; its specification is still experimental.

---

## 11. OTLP exporter

OTLP is stable for metrics, traces, and logs.

NodeScope does not need an OpenTelemetry SDK in its entire core. A narrow optional exporter can translate NodeScope metrics/events into OTLP.

Possible mapping:

- host metrics -> OTLP metrics;
- NodeScope events -> OTLP logs/events;
- incidents -> structured logs;
- NodeScope self-telemetry -> OTLP metrics.

Use HTTP first if it keeps implementation/dependencies simpler. gRPC can remain optional.

---

## 12. NodeScope self-observability

The monitoring system must monitor itself.

Expose:

- heartbeat requests/sec;
- heartbeat failures;
- request latency;
- active agents;
- stale agents;
- history-store bytes;
- history write latency;
- WAL backlog;
- WAL dropped records;
- event count;
- incident count;
- MCP calls by tool;
- MCP errors;
- API auth failures;
- state persistence duration;
- collector duration/error counts.

These should be available to:

- CLI doctor;
- /metrics;
- MCP get_system_health;
- internal alerting for severe NodeScope degradation.

---

## 13. Service checks v2

Current: TCP, HTTP, systemd.

Potential additions:

- DNS resolution;
- ICMP/ping where privileges permit;
- TLS certificate expiry;
- HTTPS response-body matcher;
- HTTP expected status/range;
- HTTP headers;
- command check with strict local allowlist only;
- Windows service check;
- file existence/age check;
- port connect + TLS handshake.

Avoid general arbitrary shell execution in normal service checks.

A typed, explicit check is easier to secure and explain to AI.

---

## 14. Discovery

NodeScope does not need cloud-scale discovery immediately.

Progressive options:

1. explicit join, current model;
2. labels/groups;
3. file-based target/config import;
4. DNS SRV discovery;
5. optional environment-specific discovery adapters.

Prometheus file-based discovery is a useful pattern: a simple generated JSON/YAML file becomes the boundary to any external discovery system.

NodeScope should prefer adapters rather than hard-coding many cloud APIs into the core binary.

---

## 15. MCP evolution

Keep the current AI-first strategy: compact high-level tools before raw inventory.

Next read tools:

```text
get_metric_history
get_node_trend
compare_nodes
get_capacity_risk
get_fleet_changes
get_system_health
explain_incident
```

Rules:

- outputs must remain bounded;
- every history tool requires a time window;
- return aggregates before raw samples;
- expose evidence with conclusions;
- distinguish observed fact, derived metric, and heuristic estimate;
- do not invent root cause.

Future transport:

- keep stdio;
- add stateless HTTP MCP once the server API is ready for remote AI access;
- preserve the same tool semantics across both transports.

---

## 16. Capacity analysis

Once history exists, add deterministic forecasting before ML.

Examples:

- disk fill ETA from recent growth;
- sustained memory growth;
- increasing service latency;
- recurring hourly/daily load windows.

Start with:

- linear trend;
- robust slope;
- moving average;
- min/max range;
- confidence flags based on sample count and fit quality.

Avoid calling a weak linear extrapolation "AI prediction".

MCP output should say:

```text
Observed: disk used increased 18.2 GiB over 7 days.
Estimated: at the recent linear rate, 90% capacity is ~12 days away.
Confidence: low/medium/high.
```

---

## 17. OpAMP-inspired management model

OpAMP is a useful architecture reference for NodeScope v2.

Concepts worth adopting before implementing the protocol itself:

- AgentCapabilities;
- AgentIdentification;
- AgentHealth;
- EffectiveConfig;
- DesiredConfig;
- ConfigHash;
- ConfigApplyStatus;
- package/update status;
- negotiated heartbeat interval;
- explicit capability negotiation.

This lets future NodeScope management remain typed.

Do **not** jump directly to arbitrary remote shell.

Future safe operations can be capabilities such as:

```text
restart_service
reload_service
apply_monitoring_config
rotate_node_credential
update_nodescope_agent
rollback_nodescope_agent
```

Each action should have:

- capability check;
- authorization scope;
- explicit request;
- audit event;
- timeout;
- result;
- idempotency model;
- rollback where meaningful.

---

## 18. mTLS and certificate lifecycle

Token authentication is adequate for the current private-infrastructure scope, but a management plane needs stronger node identity.

Future security work:

- optional mTLS;
- per-agent certificate;
- certificate rotation;
- certificate revocation;
- short-lived enrollment token;
- key pinning/trust policy;
- signed update packages.

Keep token mode for small/simple deployments.

---

## 19. Signed agent updates

If NodeScope ever updates agents remotely:

- manifest includes version, platform, hash, size;
- binary/package hash verified before install;
- signature verified against pinned release key;
- stage to temporary path;
- self-check;
- atomic swap where possible;
- old binary retained for rollback;
- update status reported centrally.

Never make "download URL + execute" the update mechanism.

---

## 20. Optional eBPF collector

eBPF can expose valuable Linux observability:

- TCP connect latency;
- retransmits;
- socket failures;
- per-process network activity;
- scheduler latency;
- syscall latency;
- block I/O latency.

But it adds major complexity:

- kernel/version compatibility;
- privileges/capabilities;
- verifier behavior;
- BTF/CO-RE concerns;
- larger build/release surface.

Therefore:

**eBPF must not be required for NodeScope core.**

Treat it as an optional advanced collector or companion module after Collector v2 and history are stable.

---

## 21. Dependency graph

A later version can model service dependencies:

```text
web -> api -> database
          -> redis
```

When database fails:

- database alert remains primary;
- API failures may be marked downstream;
- web failures may be marked downstream;
- incident view can show correlated dependency impact.

This should never suppress raw evidence. Dependency inference must be explicit/configured, not guessed by an LLM.

---

## 22. Event and incident fingerprints

Add stable fingerprints for repeated patterns:

```text
node + kind + subject + policy
```

Uses:

- detect recurring incidents;
- compare this outage with previous ones;
- count recurrence;
- reduce duplicate notifications;
- MCP: "has this happened before?"

Possible command:

```text
nodescope incident similar <id>
```

---

# Technologies intentionally deferred

## Full PromQL

Not needed. It would turn NodeScope into a Prometheus reimplementation.

## Full tracing backend

NodeScope should export traces/OTLP when useful, not become Jaeger/Tempo.

## Full log aggregation

Logs may be attached to incidents later through bounded snippets or external references, but NodeScope should not become Loki.

## Kubernetes-first architecture

Adapters can come later. NodeScope's strength is ordinary machines and mixed fleets.

## Arbitrary remote shell

Conflicts with the safe AI/operator model. Prefer typed actions.

## Mandatory eBPF

Too much platform complexity for the core product.

## Prometheus Remote Write 2.0 as the primary protocol

Useful later, but currently experimental and unnecessary for the core architecture.

---

# Recommended architecture evolution

```text
                     +--------------------+
                     | CLI / HTTP / MCP   |
                     +----------+---------+
                                |
                     +----------v---------+
                     |   NodeScope Core   |
                     | alerts / incidents |
                     | diagnosis / query  |
                     +-----+---------+----+
                           |         |
              +------------+         +----------------+
              |                                       |
    +---------v----------+                  +---------v---------+
    | current state JSON |                  | metric history    |
    | config / auth      |                  | WAL + rollups     |
    +--------------------+                  +-------------------+

                    HTTPS / future mTLS
                           ^
                           |
               +-----------+-----------+
               |                       |
       +-------+-------+       +-------+-------+
       | NodeScope     |       | NodeScope     |
       | Agent         |       | Agent         |
       | collectors    |       | collectors    |
       | local WAL     |       | local WAL     |
       +---------------+       +---------------+

Export adapters:
  /metrics -> Prometheus/OpenMetrics
  OTLP     -> OTel Collector/backend

Future management:
  desired config / capabilities / typed actions
```

---

# Research references

- OpenTelemetry OTLP specification: https://opentelemetry.io/docs/specs/otlp/
- OpenTelemetry system metric semantic conventions: https://opentelemetry.io/docs/specs/semconv/system/system-metrics/
- OpenTelemetry host resource conventions: https://opentelemetry.io/docs/specs/semconv/resource/host/
- OpenTelemetry OpAMP specification: https://opentelemetry.io/docs/specs/opamp/
- Prometheus exposition formats: https://prometheus.io/docs/instrumenting/exposition_formats/
- Prometheus storage/WAL design: https://prometheus.io/docs/prometheus/latest/storage/
- Prometheus remote-write 2.0 specification: https://prometheus.io/docs/specs/prw/remote_write_spec_2_0/
- Prometheus file-based discovery: https://prometheus.io/docs/prometheus/latest/configuration/configuration/
- Linux PSI documentation: https://docs.kernel.org/accounting/psi.html
- eBPF documentation: https://docs.ebpf.io/
