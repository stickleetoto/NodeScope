# jjp v1.0.0 — Observe

This is the first production-oriented V1 release of jjp.

## Scope

V1 is the observability generation: agent telemetry, service health, alerts/events/incidents, CLI, HTTP API v1 and MCP. It intentionally does not include AI-controlled remote server operations.

## Stabilization completed

- Versioned central state schema with v0.6 legacy migration and pre-migration backup
- Persisted alert policy and stable join-token lifecycle
- Atomic durable state/config replacement
- Cross-process central state locking
- State validation, backup and guarded restore
- Join/read/admin token rotation
- Optional native TLS
- Request, payload and heartbeat validation limits
- Node deletion incident cleanup
- Corrupt/future state protection and legacy orphan repair
- Windows metric collection timeout
- Hardened server timeouts and shutdown flush ordering
- Release checksums and hardened CI/build scripts

## Verification

Completed on the release source:

- `go test ./... -count=1` — PASS
- `go test -race ./... -count=1` — PASS
- `go vet ./...` — PASS
- Linux amd64 build — PASS
- Linux arm64 build — PASS
- Windows amd64 cross-build — PASS
- Accelerated 24-node virtual soak with outages/service failure/resource pressure/restarts — PASS
- Real-process state-lock / token-rotation / backup / restore / policy-persistence E2E — PASS
- Real-process join / heartbeat / incident / recovery / doctor / MCP E2E — PASS
- Central restart with existing node credential reuse — PASS
- Native TLS startup and `/healthz` smoke — PASS

See `SECURITY.md` and `docs/OPERATIONS.md` before production deployment.
