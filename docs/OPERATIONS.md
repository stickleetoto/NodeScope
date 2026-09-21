# NodeScope operations guide

## Recommended topology

Run one central server on a stable machine and one nodescope agent per monitored node. Agents make outbound HTTP(S) requests to the central server; the central server does not open connections back to agents in v1.

For remote networks, use TLS or a trusted VPN/overlay network.

## Central server

Example production-style start:

```bash
nodescope host \
  --listen :7443 \
  --tls-cert /etc/nodescope/server.crt \
  --tls-key /etc/nodescope/server.key
```

Alert policy flags are persisted in central state. After setting them once, later starts can omit them and reuse the stored values.

## Linux systemd example for the central server

Create `/etc/systemd/system/nodescope-server.service`:

```ini
[Unit]
Description=NodeScope central server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/nodescope host --listen :7443 --data /var/lib/jjp/server.json --tls-cert /etc/nodescope/server.crt --tls-key /etc/nodescope/server.key
Restart=on-failure
RestartSec=3
User=jjp
Group=jjp
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/jjp

[Install]
WantedBy=multi-user.target
```

Adjust certificate paths and service hardening to match your environment.

## Daily checks

```bash
nodescope overview
nodescope alerts
nodescope incidents --status open
nodescope doctor
```

For AI clients, start with `get_overview`; historical questions should normally use `get_incidents` before raw events.

## Backup

A live backup is safe because the state file is replaced atomically:

```bash
nodescope state check
nodescope state backup
```

Backups contain credentials and node secrets. Protect them like the live state file.

## Restore

Stop the central server first. Restore refuses to proceed while another nodescope host owns the same state lock.

```bash
nodescope state check --data /path/to/backup.bak
nodescope state restore /path/to/backup.bak --force
nodescope state check
```

Before replacement, nodescope automatically saves the current valid state as a timestamped `.pre-restore-...bak` file.

## Token rotation

```bash
export JJP_ADMIN_TOKEN=...
nodescope token rotate join
nodescope token rotate read
nodescope token rotate admin
```

After rotating the read token, update CLI/MCP environments. After rotating the admin token, update administrative environments immediately. Rotating the join token does not affect already-registered nodes.

## Agent recovery

The agent retries transient network failures with exponential backoff. If a node registration was deleted from central state, register that node again with a current join token.

## Disk or state failure

If the server refuses to start because state is malformed or from a newer schema:

1. Do not edit the production state in place.
2. Run `nodescope state check --data PATH`.
3. Preserve the failed file.
4. Restore a known-good backup if available.
5. Start the central server and run `nodescope doctor` plus `nodescope overview`.

A v0.6-or-earlier state is migrated automatically on first open and the original bytes are preserved as `server.json.schema0.bak`.

## v1.1 fleet health probe

For a compact operational check:

```bash
nodescope health
```

Exit codes:

- `0`: healthy
- `2`: degraded/critical (attention required)
- `1`: command/connectivity/authentication error

For machine-readable automation:

```bash
nodescope health --json
```

Bound historical troubleshooting to a useful window instead of retrieving unrelated history:

```bash
nodescope incidents --since 24h
nodescope events --since 2h --limit 100
```
