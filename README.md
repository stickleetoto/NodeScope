# jjp

**jjp (Jjamppong)** is a lightweight, CLI-first distributed node observability system for humans and AI clients.

It intentionally has **no web dashboard**. A central server receives authenticated outbound heartbeats from agents and exposes the resulting health model through:

- CLI
- HTTP API v1
- stdio MCP for Claude/other MCP clients

JJP v1 is the **Observe** generation: it gives operators and AI reliable visibility across servers. It does not execute remote shell commands or autonomously manage nodes.

## v1.1 highlights

- `jjp health` gives a one-line fleet health probe and exit code `2` when attention is required
- `jjp events --since 24h` and `jjp incidents --since 24h` bound historical queries by time
- MCP history tools accept `since_minutes` so AI clients can answer time-window questions without reading unrelated history
- Release/package metadata is prepared for GitMake-managed repository updates and GitHub Releases

- Single Go binary, zero external Go dependencies
- Linux amd64, Linux arm64 (Raspberry Pi), Windows amd64 builds
- CPU, RAM, disk, uptime and available temperature telemetry
- TCP, HTTP and Linux systemd service checks
- ONLINE / UNSTABLE / OFFLINE node health
- Persistent alerts, events and correlated incidents
- Human-first `overview`, `diagnose`, `doctor`
- Machine-readable `--json`
- AI-first MCP with structured output schemas and read-only default behavior
- Versioned HTTP API v1 with read/admin credential separation
- Persistent state schema with automatic legacy migration
- Same-filesystem atomic state/config replacement
- Exclusive central-state lock to prevent two hosts from corrupting one state file
- State validation, live backup and guarded restore
- Explicit join/read/admin token rotation
- Optional native HTTPS/TLS
- Input/body limits and heartbeat validation
- Bounded retention: newest 5,000 raw events and 2,000 incidents
- Accelerated multi-node soak/restart regression coverage

## Architecture

```text
node agent ──outbound heartbeat──▶ jjp central ◀── CLI
                                      ▲
                                      ├── HTTP API v1
                                      └── MCP ──▶ AI client
```

The central server never needs to open a connection back to an agent in v1.

## Quick start

### 1. Start central

```bash
jjp host
```

On the **first** start, jjp prints the generated join/read/admin tokens once. Later starts do not print existing secrets into service logs.

```text
Jjamppong central server
Listen:      :7443
State:       .../jjp/server.json
Schema:      1
Transport:   HTTP (...)
Join token:  ...
Read token:  ...
Admin token: ...
```

To view stored tokens intentionally on the central machine:

```bash
jjp token show
jjp token show --kind join
```

For remote/untrusted networks, use TLS:

```bash
jjp host --listen :7443 \
  --tls-cert /etc/jjp/server.crt \
  --tls-key /etc/jjp/server.key
```

Plain HTTP should be limited to a trusted private LAN, loopback or trusted VPN/overlay network.

### 2. Join a node

```bash
jjp join https://SERVER:7443 JOIN_TOKEN --name pi-main
```

The join command stores the per-node secret in the local agent config and begins sending heartbeats. Later:

```bash
jjp agent
```

Linux/systemd agent installation:

```bash
jjp install-agent
# or system-wide
sudo jjp install-agent --system --config /home/USER/.config/jjp/agent.json
```

### 3. Inspect the fleet

```bash
export JJP_SERVER=https://SERVER:7443
export JJP_API_TOKEN=READ_TOKEN

jjp overview
jjp health
jjp ls
jjp alerts
jjp incidents
jjp events --limit 20
jjp show pi-main
jjp diagnose pi-main
jjp doctor
```

Machine-readable output:

```bash
jjp overview --json
jjp health --json
jjp diagnose pi-main --json
jjp ls --json
jjp show pi-main --json
jjp alerts --json
jjp incidents --json
jjp incident INCIDENT_ID --json
jjp events --json
jjp doctor --json
```

## Fleet health probe

`jjp health` is intended for shell scripts, service checks, and quick operator checks:

```bash
jjp health
jjp health --json
```

It prints only the fleet-level status/headline. Exit code `0` means healthy. Exit code `2` means the fleet is degraded or critical and attention is required. Connection/configuration errors continue to use exit code `1`.

## Services

Service checks are configured on each agent.

```bash
jjp service add bio --tcp 127.0.0.1:8787
jjp service add api --http http://127.0.0.1:8080/healthz
jjp service add nginx --systemd nginx
jjp service ls
jjp service rm bio
```

Restart the agent after editing service checks.

## Alerts, events and incidents

The data model is intentionally layered:

```text
latest metrics
    ↓
active alerts + transition events
    ↓
correlated incidents
```

Default policy:

```text
CPU >= 90% for 5m      warning
RAM >= 90% for 5m      warning
Disk >= 90% for 5m     warning
heartbeat age > 15s    UNSTABLE / warning
heartbeat age > 60s    OFFLINE / critical
service check DOWN      critical
```

Custom policy values are persisted in central state:

```bash
jjp host \
  --cpu-alert 85 \
  --ram-alert 90 \
  --disk-alert 95 \
  --metric-for 3m \
  --unstable-after 20s \
  --offline-after 90s
```

Later starts can omit those flags and reuse the persisted policy.

Incident correlation is **evidence-only**. Temporal proximity is not presented as a proven root cause.

```bash
jjp incidents --status open
jjp incidents --status resolved --node pi-main
jjp incidents --since 24h
jjp events --since 2h --limit 100
jjp incident INCIDENT_ID
```

## MCP / AI usage

Start a read-only stdio MCP server:

```bash
JJP_SERVER=https://SERVER:7443 \
JJP_API_TOKEN=READ_TOKEN \
jjp mcp
```

Read-only tools include:

```text
get_overview
diagnose_node
get_summary
get_unhealthy_nodes
get_active_alerts
get_incidents
get_incident
get_recent_events
get_node
list_services
list_nodes
```

Recommended AI flow:

```text
current fleet question  -> get_overview
one-node question       -> diagnose_node / get_node
historical question     -> get_incidents -> get_incident
raw detail only if needed -> get_recent_events
```

For time-bounded historical questions, `get_incidents` and `get_recent_events` accept `since_minutes` (for example `1440` for the last 24 hours).

Administrative registry tools can be exposed explicitly:

```bash
JJP_ADMIN_TOKEN=ADMIN_TOKEN jjp mcp --allow-write
```

This adds `rename_node` and `remove_node`; `remove_node` also requires `confirm=true`.

Generic MCP configuration:

```json
{
  "mcpServers": {
    "jjp": {
      "command": "/absolute/path/to/jjp",
      "args": ["mcp"],
      "env": {
        "JJP_SERVER": "https://SERVER:7443",
        "JJP_API_TOKEN": "READ_TOKEN"
      }
    }
  }
}
```

MCP protocol traffic is written only to stdout; diagnostics use stderr.

## HTTP API v1

```text
GET    /healthz                         public
GET    /api/v1/info                     public
POST   /api/v1/join                     join token
POST   /api/v1/heartbeat                node credentials

GET    /api/v1/summary                  read/admin
GET    /api/v1/overview                 read/admin
GET    /api/v1/unhealthy                read/admin
GET    /api/v1/alerts                   read/admin
GET    /api/v1/events                   read/admin
GET    /api/v1/incidents                read/admin
GET    /api/v1/incidents/{id}           read/admin
GET    /api/v1/nodes                    read/admin
GET    /api/v1/nodes/{id}               read/admin
GET    /api/v1/nodes/{id}/services      read/admin
GET    /api/v1/nodes/{id}/diagnosis     read/admin

PATCH  /api/v1/nodes/{id}               admin
DELETE /api/v1/nodes/{id}               admin
POST   /api/v1/tokens/{kind}/rotate     admin
```

`kind` is `join`, `read`, or `admin`.

Responses include `X-Request-ID`, `X-JJP-Version`, and `X-JJP-API-Version`. Structured errors contain a stable error code and request ID.

## Credentials and rotation

- **Join token**: registers new agents. It now survives central restarts until explicitly rotated.
- **Read token**: read-only API/CLI/MCP access.
- **Admin token**: administrative API access.
- **Node secret**: one agent's heartbeat credential.

```bash
export JJP_ADMIN_TOKEN=...
jjp token rotate join
jjp token rotate read
jjp token rotate admin
```

Rotation is immediate. Already-registered node secrets are unaffected by rotating the join token.

## State integrity, backup and restore

Default central state is the OS user config directory's `jjp/server.json`. Agent config is `jjp/agent.json`.

The central state contains credentials and node secrets. Never commit or publish it or its backups.

Validate:

```bash
jjp state check
jjp state check --json
```

Live backup:

```bash
jjp state backup
jjp state backup --out /secure/path/jjp.bak
```

Restore requires an explicit destructive flag and refuses to run while a central process owns the same state lock:

```bash
# stop jjp host first
jjp state restore /secure/path/jjp.bak --force
```

The current valid state is preserved automatically as a timestamped pre-restore backup before replacement.

State schema is currently **1**. v0.6 and earlier states are migrated on first open; the exact legacy bytes are preserved as `server.json.schema0.bak`. See [docs/MIGRATION.md](docs/MIGRATION.md).

## Doctor

```bash
jjp doctor
```

Doctor verifies central reachability, API version, server/client version, state schema, read authentication and local agent configuration.

## Build and verification

Requirements: Go 1.23+.

```bash
go test ./...
go test -race ./...
go vet ./...
```

Release binaries:

```bash
./build.sh
# or on PowerShell
./build.ps1
```

Both build scripts produce:

```text
dist/jjp-linux-amd64
dist/jjp-linux-arm64
dist/jjp-windows-amd64.exe
dist/SHA256SUMS.txt
```

GitHub Actions runs tests, race detection, vet, all three cross-builds and checksums.

## Operational documentation

- [Operations guide](docs/OPERATIONS.md)
- [Migration guide](docs/MIGRATION.md)
- [Security notes](SECURITY.md)

## Scope boundary

**JJP v1 = Observe.**

It collects, validates, correlates and exposes server health. It does not provide AI-controlled remote service restart, shell, filesystem, package-manager, deployment or rollback operations.

Those control-plane capabilities are intentionally reserved for a future V2 design with explicit policy, permissions, approval, audit and recovery semantics.
