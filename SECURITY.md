# Security

nodescope is designed for trusted operators and private infrastructure. It has no browser UI and does not require a cloud service.

## Transport

The central server supports native TLS:

```bash
nodescope host --listen :7443 --tls-cert /etc/nodescope/server.crt --tls-key /etc/nodescope/server.key
```

Plain HTTP should be used only on a trusted private LAN, loopback, or inside a trusted VPN/overlay network. Do not expose an unauthenticated plain-HTTP path to the public Internet.

## Credentials

nodescope separates credentials by purpose:

- Join token: registers new nodes.
- Read token: read-only CLI/API/MCP access.
- Admin token: administrative API access.
- Node secret: heartbeat authentication for one node.

Rotate a credential when exposure is suspected:

```bash
nodescope token rotate join
nodescope token rotate read
nodescope token rotate admin
```

Token rotation is immediate. Clients using the previous token will stop authenticating.

The central state file contains all central credentials and node secrets. Keep it private. nodescope writes it with owner-only permissions where the OS supports Unix-style permissions. Backups contain the same secrets and require the same protection.

## MCP

`nodescope mcp` is read-only by default. Write tools are exposed only with `--allow-write` and an admin token. The destructive `remove_node` MCP tool additionally requires `confirm=true`.

NodeScope v1 does not execute remote shell commands on managed nodes. Remote AI-controlled operations are intentionally outside the v1 scope.

## State integrity

Central state uses a versioned schema, bounded retention, same-filesystem atomic replacement, and an exclusive host lock. `nodescope state check` validates a state file before use. `nodescope state restore` refuses to run while a central server holds the same state lock and creates a pre-restore backup before replacement.

## Reporting issues

When reporting a security issue, do not include real join/read/admin tokens, node secrets, private hostnames, or a production `server.json` file.
