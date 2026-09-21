> **Rename note:** The project was renamed to **NodeScope** after the v1.1.0 release. This file preserves the original JJP v1.1.0 release naming and asset names for historical accuracy.

# JJP v1.1.0 — Operational UX

JJP v1.1.0 is a backwards-compatible V1 maintenance release focused on operator and AI query ergonomics. It does not add V2 remote-control capabilities.

## Highlights

- Added `jjp health` for a compact fleet health probe.
  - exit `0`: healthy
  - exit `2`: degraded or critical / operator attention required
  - exit `1`: connection, authentication, configuration, or other command error
  - supports `--json`
- Added bounded history queries:
  - `jjp events --since 2h`
  - `jjp incidents --since 24h`
  - RFC3339 timestamps are also accepted by the CLI
- HTTP API v1 history endpoints now accept an optional RFC3339 `since` parameter.
- MCP `get_incidents` and `get_recent_events` now accept `since_minutes` so AI clients can answer time-window questions with less irrelevant context.
- Added dedicated GitMake release intent metadata for a managed repository update plus GitHub Release.

## Compatibility

- HTTP API remains `v1`.
- State schema remains `1`; no state migration is required from JJP v1.0.0.
- Existing v1.0.0 agents and central state remain compatible.
- No server-control or arbitrary remote execution features are included; those remain outside the V1 scope.

## Release assets

- `jjp-windows-amd64.exe`
- `jjp-linux-amd64`
- `jjp-linux-arm64`
- `SHA256SUMS.txt`
