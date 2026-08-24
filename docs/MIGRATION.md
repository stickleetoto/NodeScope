# Migration to the v1 state format

jjp v1 uses central state schema `1`.

## From v0.6.x and earlier

Those releases did not write a `schema_version` or persist alert-policy configuration. On first start with the stabilized v1 code:

1. jjp reads the old state without modifying it.
2. The original bytes are copied to `server.json.schema0.bak` with private permissions.
3. Existing join/read/admin credentials, node secrets, nodes, alerts, events, incidents, and pending metric timers are retained.
4. The state is written as schema 1 using atomic replacement.
5. Legacy orphan alerts and open incidents for nodes that were already deleted are repaired during load.

Because v0.6 did not persist custom alert policy flags, a custom policy must be supplied once during the first upgraded start. It is persisted from then on:

```bash
jjp host --cpu-alert 85 --ram-alert 90 --disk-alert 95 --metric-for 3m --unstable-after 20s --offline-after 90s
```

Later starts can simply use `jjp host`; the stored policy is reused.

## Before upgrading

Recommended:

```bash
jjp state backup --out pre-upgrade.bak
```

For a pre-v0.7 binary that does not have `state backup`, stop the old central server and copy `server.json` manually while preserving file permissions.

## Downgrade

Do not point an older binary at a schema-1 state file. Keep the pre-migration backup if rollback might be required.
