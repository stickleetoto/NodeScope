# NodeScope Roadmap

This roadmap evolves NodeScope from a lightweight node monitor into an AI-native fleet observability and controlled-management platform while preserving a small operational footprint.

## North star

```text
v1.x: Observe deeply
v2.x: Manage safely
```

NodeScope should be able to answer:

1. What is broken now?
2. What changed?
3. Is this getting worse?
4. Has this happened before?
5. What should the operator inspect next?
6. Later: what safe, typed action is explicitly authorized?

---

# Phase 0 — Rename closeout

Target: immediately

Goal: make NodeScope the canonical identity before new architecture work.

## Tasks

- merge the NodeScope rebrand PR;
- move `cmd/jjp` to `cmd/nodescope`;
- change Go module/import identity from the legacy JJP path;
- change API `info.name` and other remaining public identity strings;
- retain legacy v1 headers and disk paths only where they are intentional compatibility contracts;
- document legacy compatibility boundaries;
- add NodeScope repository description/topics;
- create a post-rename release.

## Exit criteria

- default branch README says NodeScope;
- binary is named `nodescope`;
- no accidental user-facing JJP/Jjamppong strings remain;
- CI passes;
- old state can still be opened.

---

# v1.2 — Telemetry Foundation

Theme: **history + better host signals + loss-resistant agents**

This is the most important technical release after the rename.

## 1. Metric model v2

- typed samples;
- unit metadata;
- gauge/counter distinction;
- timestamp;
- bounded attributes;
- collector capability reporting.

## 2. Collector v2

Linux:

- CPU modes;
- load average;
- memory + swap;
- filesystem per mount;
- disk I/O;
- network interfaces;
- process counts;
- temperature/hwmon;
- PSI CPU/memory/I/O.

Windows:

- CPU;
- RAM/pagefile;
- logical disks;
- network;
- processes;
- Windows service health.

## 3. Historical metrics

Implement bounded local history:

- raw short retention;
- 1m rollups;
- 5m rollups;
- 1h rollups;
- min/max/avg/last/count;
- segment checksums;
- crash-safe append;
- retention cleanup.

## 4. Agent WAL

- local append queue;
- sequence IDs;
- central durable ACK;
- bounded age/size;
- retry;
- dropped-data counter;
- idempotent replay.

## 5. History query API

- node/time/metric filtering;
- bounded result size;
- server-side aggregation;
- no unbounded raw dump endpoint.

## 6. MCP history tools

- `get_metric_history`;
- `get_node_trend`;
- `get_resource_peaks`;
- `compare_nodes`.

## 7. Self-observability baseline

Track NodeScope itself:

- request count/errors;
- agent count;
- history store size;
- WAL backlog;
- collector failures;
- persistence latency.

## v1.2 exit criteria

A user can ask:

> "Did pi-main's memory steadily rise over the last 12 hours?"

and NodeScope can answer from stored evidence, not only from current state.

---

# v1.3 — Fleet Operations and Interoperability

Theme: **organize the fleet and connect to the existing observability ecosystem**

## 1. Labels and groups

- bounded key/value labels;
- group membership;
- CLI/API/MCP filtering;
- policies by group.

## 2. Alert engine v2

- hysteresis;
- per-group thresholds;
- maintenance windows;
- silences with expiry;
- notification cooldown;
- stable alert fingerprints.

## 3. Notification adapters

Start small:

- generic webhook;
- Discord webhook;
- optional Slack-compatible webhook format.

Notifications contain references and evidence, not massive telemetry dumps.

## 4. Prometheus/OpenMetrics endpoint

- `GET /metrics`;
- fleet health;
- current node metrics;
- NodeScope self-metrics;
- no PromQL implementation.

## 5. File/DNS discovery adapters

- import labels/targets from generated file;
- DNS SRV discovery where useful;
- retain explicit join as default.

## 6. Service checks v2

- TLS certificate expiry;
- DNS;
- expected HTTP status;
- body substring/regex with strict limits;
- Windows services;
- file age/existence;
- optional ICMP where platform permissions permit.

## 7. Remote MCP transport

- preserve stdio;
- add stateless HTTP MCP;
- same tool contracts;
- explicit auth and network exposure guidance.

## v1.3 exit criteria

NodeScope can operate a mixed lab/server fleet, integrate with Prometheus/Grafana, and notify external systems without requiring any of them for core operation.

---

# v1.4 — Reliability and Scale

Theme: **make thousands of nodes boring**

## 1. Heartbeat/telemetry separation

- tiny liveness heartbeat;
- batched telemetry shipment;
- negotiated/adaptive intervals.

## 2. Backpressure

- bounded queues;
- server overload signal;
- retry-after;
- jitter;
- adaptive upload rate.

## 3. History compaction

- background segment compaction;
- bounded CPU/I/O budget;
- safe partial-failure recovery;
- compaction metrics.

## 4. Large-fleet query efficiency

- indexes for node name/label/group;
- bounded incident indexes;
- precomputed fleet summaries;
- cursor pagination.

## 5. Central persistence evolution

Keep:

- small JSON for config/auth/policy.

Separate:

- metric history;
- event/incident append data where scale requires it.

Avoid replacing everything with one database unless measurements justify it.

## 6. Protocol compatibility policy

Formalize:

- API versioning;
- agent compatibility window;
- metric schema compatibility;
- feature capabilities;
- deprecation timeline.

## v1.4 exit criteria

NodeScope can handle a large fleet without every heartbeat rewriting a monolithic state file or every query scanning all historical data.

---

# v1.5 — Evidence-Based Intelligence

Theme: **derive useful answers without pretending guesses are facts**

## 1. Capacity risk

- disk fill ETA;
- memory growth detection;
- increasing service latency.

## 2. Trend engine

- robust slopes;
- moving averages;
- change points;
- sample quality/confidence.

## 3. Recurring incident fingerprints

- detect repeated incident patterns;
- similar-incident lookup;
- recurrence count;
- previous resolution context.

## 4. Fleet comparison

- compare nodes in the same group;
- identify outliers;
- compare pre/post deployment windows.

## 5. Dependency graph

Configured dependencies:

```text
web -> api -> database
```

Use them to add impact context and suppress redundant notifications, never to delete evidence.

## 6. MCP analytical tools

- `get_capacity_risk`;
- `find_outlier_nodes`;
- `find_similar_incidents`;
- `compare_time_windows`;
- `get_fleet_changes`.

## v1.5 exit criteria

NodeScope can answer:

> "Which nodes are behaving differently from the rest?"
>
> "Has this outage happened before?"
>
> "Which disk is likely to need attention soon?"

with traceable evidence and confidence labels.

---

# v1.6 — OpenTelemetry Bridge

Theme: **export, do not replace**

## 1. OTLP exporter

Optional:

- metrics;
- NodeScope events as logs;
- NodeScope self-telemetry.

## 2. Semantic mapping

Maintain explicit mappings from NodeScope metrics to OTel conventions.

Do not silently rename historical metrics when upstream conventions change.

## 3. External backend profiles

Document tested paths for:

- OpenTelemetry Collector;
- Grafana ecosystem;
- other OTLP-compatible backends.

## v1.6 exit criteria

NodeScope can remain standalone or become a lightweight edge/fleet source feeding a larger observability stack.

---

# v2.0 — Safe Management Plane

Theme: **observe + explicitly authorized typed control**

Design this using lessons from OpAMP.

## Agent protocol additions

- AgentCapabilities;
- AgentIdentification;
- health/status;
- DesiredConfig;
- EffectiveConfig;
- ConfigHash;
- ConfigApplyStatus;
- heartbeat interval negotiation;
- package/update status.

## Security

- optional mTLS;
- certificate enrollment/rotation;
- scoped admin credentials;
- audit log;
- signed update manifests.

## Allowed typed operations

Initial candidates:

- restart a named monitored service;
- reload a named service;
- apply NodeScope monitoring configuration;
- rotate a node credential;
- update NodeScope agent;
- rollback NodeScope agent.

Explicitly not included by default:

- arbitrary shell;
- arbitrary filesystem write;
- arbitrary package manager execution.

## Action contract

Every mutation requires:

- advertised capability;
- authenticated authorization;
- explicit operator/AI-user request;
- unique action ID;
- timeout;
- audit event;
- deterministic result;
- idempotency classification;
- rollback plan where meaningful.

## v2.0 exit criteria

An AI can safely request a narrow operation like:

> "Restart the monitored API service on pi-main."

without gaining general remote-shell access.

---

# v2.1+ — Advanced / Optional

## eBPF collector

Optional Linux module for:

- network latency;
- retransmits;
- scheduler latency;
- syscall/block-I/O latency.

Never required by core.

## Hardware collectors

Potential:

- GPU;
- NVMe SMART/health;
- fans;
- power;
- battery/UPS;
- sensors.

## Container/Kubernetes adapters

Only after the ordinary-host experience is mature.

---

# Priority map

```text
NOW
 |
 +-- P0  Rebrand closeout
 |
 +-- P1  Metric model v2
 |       Collector v2
 |       PSI
 |       History store
 |       Agent WAL
 |       History MCP
 |
 +-- P2  Labels/groups
 |       Alert v2
 |       Webhooks
 |       /metrics
 |       Service checks v2
 |       HTTP MCP
 |
 +-- P3  Scale/backpressure
 |       Compaction
 |       Large-fleet indexes
 |       Capacity/trend analysis
 |       Similar incidents
 |
 +-- P4  OTLP bridge
 |
 +-- P5  v2 management plane
 |
 +-- P6  Optional eBPF / hardware / K8s
```

---

# What not to do yet

Do not spend the next development cycle on:

- a large web dashboard;
- PromQL;
- full log ingestion;
- arbitrary remote shell;
- Kubernetes operator;
- mandatory eBPF;
- distributed central-server clustering;
- ML anomaly models.

Those are expensive before the history/collector/WAL foundation exists.

---

# Suggested immediate issue sequence

1. Rebrand closeout.
2. Define metric model v2.
3. Implement collector registry.
4. Add Linux network/disk/swap/load collectors.
5. Add Linux PSI collector.
6. Design metric history segment format.
7. Implement raw history append/recovery.
8. Add rollup compaction.
9. Implement agent WAL + ACK protocol.
10. Add history API.
11. Add MCP trend/history tools.
12. Add self-observability metrics.
13. Cut NodeScope v1.2.

That sequence minimizes throwaway work: the metric model lands before collectors/history/exporters are built on top of it.
