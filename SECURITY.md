# Security

jjp is designed for trusted operators and private infrastructure. It has no browser UI and does not require a cloud service.

## Transport

The central server supports native TLS:

```bash
jjp host --listen :7443 --tls-cert /etc/jjp/server.crt --tls-key /etc/jjp/server.key
```

Plain HTTP should be used only on a trusted private LAN, loopback, or inside a trusted VPN/overlay network. Do not expose an unauthenticated plain-HTTP path to the public Internet.

## Credentials

jjp separates credentials by purpose:

- Join token: registers new nodes.
- Read token: read-only CLI/API/MCP access.
- Admin token: administrative API access.
- Node secret: heartbeat authentication for one node.

Rotate a credential when exposure is suspected:

```bash
jjp token rotate join
jjp token rotate read
jjp token rotate admin
```

Token rotation is immediate. Clients using the previous token will stop authenticating.

The central state file contains all central credentials and node secrets. Keep it private. jjp writes it with owner-only permissions where the OS supports Unix-style permissions. Backups contain the same secrets and require the same protection.

## MCP

`jjp mcp` is read-only by default. Write tools are exposed only with `--allow-write` and an admin token. The destructive `remove_node` MCP tool additionally requires `confirm=true`.

JJP v1 does not execute remote shell commands on managed nodes. Remote AI-controlled operations are intentionally outside the v1 scope.

## State integrity

Central state uses a versioned schema, bounded retention, same-filesystem atomic replacement, and an exclusive host lock. `jjp state check` validates a state file before use. `jjp state restore` refuses to run while a central server holds the same state lock and creates a pre-restore backup before replacement.

## Reporting issues

When reporting a security issue, do not include real join/read/admin tokens, node secrets, private hostnames, or a production `server.json` file.
