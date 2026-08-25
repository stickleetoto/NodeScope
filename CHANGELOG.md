# Changelog

## 1.1.0 - 2026-08-25

### Added
- Added `jjp health` as a compact fleet health probe with script-friendly exit codes and JSON output.
- Added `--since` to `jjp events` and `jjp incidents` for duration/RFC3339 lookback filtering.
- Added RFC3339 `since` filtering to `/api/v1/events` and `/api/v1/incidents`.
- Added MCP `since_minutes` filtering to `get_recent_events` and `get_incidents`.
- Added project-specific GitMake release guidance for the v1.1.0 update/release flow.

### Compatibility
- HTTP API remains v1.
- State schema remains 1; no migration is required from v1.0.0.
- V1 remains observation-only; server control is intentionally deferred to V2.

## v1.0.0

- Frozen the V1 product scope as **Observe**: CLI + API + MCP, with no web UI and no remote server-control plane.
- Added state schema 1 and safe migration from v0.6-and-earlier state. The exact legacy state is preserved as `server.json.schema0.bak` before migration.
- Persisted alert policy across central restarts; omitted policy flags now preserve stored values instead of silently reverting to defaults.
- Changed join-token lifecycle so restarts preserve the current join token; rotation is explicit.
- Added `jjp token show` for deliberate local credential display and authenticated `jjp token rotate join|read|admin`.
- Added optional native HTTPS/TLS with `--tls-cert` + `--tls-key`.
- Added durable same-filesystem atomic replacement for central state and agent configuration, including Windows replace semantics and file/directory flush where supported.
- Added an exclusive state lock so two central processes cannot operate on the same state file concurrently.
- Added `jjp state check`, live `jjp state backup`, and guarded `jjp state restore --force` with automatic pre-restore preservation.
- Added state integrity validation, 64 MiB state-file safety cap, future-schema rejection, and legacy ghost-alert/incident repair.
- Added heartbeat payload validation and request/server limits to reject malformed percentages, impossible memory/disk totals, duplicate/oversized service data, and excessive headers/bodies.
- Added explicit central HTTP read/write/header timeouts and final post-shutdown state flush ordering.
- Added fixed fleet registration ceiling to constrain leaked-join-token memory abuse.
- Fixed node deletion leaving an open incident forever; node removal now closes the incident and preserves the final transition in its evidence timeline.
- Added rollback of in-memory admin mutations when durable persistence fails.
- Added a timeout around Windows PowerShell metric collection so a stuck collector cannot hang the agent indefinitely.
- Extended `jjp doctor` with state-schema compatibility checking.
- Added accelerated multi-node soak coverage with synthetic node outage, service failure, sustained resource pressure and repeated central restarts.
- Added file-lock, migration, token-persistence/rotation, backup/restore, corrupted-state preservation and ghost-incident regression tests.
- Hardened build/release scripts and CI with `go vet`, race tests, three target builds and SHA-256 manifests.
- Added security, operations and migration documentation for production deployment.

## v0.6.0

- Added persistent incident correlation above raw event history.
- Correlates overlapping alert lifecycles per node into one open incident and resolves it after the node's final active alert recovers.
- Added deterministic incident severity, title, summary, duration, event count/IDs, kinds, and affected subjects.
- Added `jjp incidents` with node/status/limit filtering and `--json`.
- Added `jjp incident <id>` with an ordered raw-event timeline and `--json`.
- Added `/api/v1/incidents` and `/api/v1/incidents/{id}` read endpoints.
- Added MCP `get_incidents` and `get_incident`; MCP guidance now prefers incidents over raw events for historical health questions.
- Added open incidents to `get_overview` / `/api/v1/overview`.
- Incident records persist across central-server restarts; retained incidents keep their own evidence timeline even after older raw events roll out, and node renames update incident display names.
- Added regression tests for outage/recovery correlation, persistence, incident API retrieval, and MCP incident retrieval.
- Explicitly treats correlation as chronology/evidence, not unsupported root-cause inference.
- Kept jjp CLI/API/MCP-only with zero external Go dependencies.

## v0.5.0

- Added `jjp overview` as the human and AI-oriented first view of central health.
- Added `jjp diagnose <node>` with deterministic findings, evidence, severity, and suggested operator checks.
- Added `jjp doctor` for server/API/authentication/local-agent-config diagnostics.
- Added machine-readable `--json` output to the major read/diagnostic CLI commands.
- Added `/api/v1/overview` and `/api/v1/nodes/{id}/diagnosis`.
- Added MCP `get_overview` and `diagnose_node` tools.
- Added MCP output schemas, structured-content-first responses, and tool behavior annotations.
- Reordered/worded MCP guidance so models prefer compact high-level tools before full inventory calls.
- Changed MCP TextContent fallback from indented JSON to compact JSON to reduce context overhead.
- Added explicit `confirm=true` gating to destructive MCP `remove_node`.
- Improved `jjp doctor` to warn when the local agent points at a different central server.
- Added regression tests for overview/diagnosis APIs, AI-oriented MCP metadata/structured content, and destructive confirmation.
- Kept jjp CLI/API/MCP-only with zero external Go dependencies.

## v0.4.0

- Added persistent active alerts and bounded event history to the central state.
- Added node heartbeat transition events for UNSTABLE, OFFLINE, and recovery.
- Added immediate service-down alerts plus service recovery/cleared events.
- Added sustained CPU/RAM/disk threshold alerts and automatic recovery events; pending threshold timers survive central-server restarts.
- Added configurable central alert policy flags: `--cpu-alert`, `--ram-alert`, `--disk-alert`, `--metric-for`, `--unstable-after`, and `--offline-after`.
- Added `jjp alerts` and `jjp events` CLI commands with node filtering and bounded event limits.
- Added read-only `/api/v1/alerts` and `/api/v1/events` endpoints.
- Added active-alert count to `/api/v1/summary` and made active metric alerts participate in unhealthy-node results.
- Added MCP tools `get_active_alerts` and `get_recent_events` without expanding write permissions.
- Kept the product CLI/API/MCP-only; no web UI was introduced.
- Event storage is capped at the newest 5,000 events.
- Added regression tests for alert lifecycle, persistence across restart, API retrieval, MCP retrieval, and recovery transitions.

## v0.3.0

- Confirmed product direction: no web dashboard; CLI + HTTP API + MCP only.
- Added a generated read-only API token separate from the admin token.
- Added `/api/v1/info`, `/summary`, `/unhealthy`, and per-node `/services` endpoints.
- Allowed read-token or admin-token access to read endpoints while keeping mutations admin-only.
- Added structured JSON API errors with stable error codes and request IDs.
- Added `X-Request-ID`, `X-JJP-Version`, and `X-JJP-API-Version` response headers.
- Moved v0.3 agent join authentication to a bearer header while retaining v0.2 join compatibility.
- Added `jjp mcp` stdio server with node/service health tools.
- Added MCP `2026-07-28` `server/discover` support plus legacy `initialize` compatibility.
- Made MCP read-only by default; `rename_node` and `remove_node` are exposed only with `--allow-write`.
- Added API client package used by MCP so MCP changes state only through the central API.
- Added tests for read/admin authorization boundaries, summary/services API, read-only MCP, modern/legacy MCP lifecycle, and MCP admin writes.

## v0.2.0

- Added TCP, HTTP, and Linux systemd service monitoring.
- Added service health output to `jjp ls` and `jjp show`.
- Added exponential-backoff reconnect behavior for agents.
- Added `jjp rename` and `jjp rm` node administration.
- Added Linux `jjp install-agent` systemd installation.
- Fixed node credentials being lost after central-server restart.
- Reduced heartbeat-related disk writes to limit unnecessary storage wear.
- Added stricter service configuration validation and constant-time node-secret comparison.
- Added Linux amd64, Linux arm64, and Windows amd64 release builds.
