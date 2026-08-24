# GitMake — LLM Usage Guide

> This file is for AI coding agents and LLMs working inside a project that uses GitMake.
> Read this file before creating/updating GitHub repositories, publishing project folders/ZIPs, or making releases.

## What GitMake is

GitMake is a high-level GitHub publishing workflow.

It accepts either:

- the current project folder, or
- a project ZIP

and turns it into a reviewed GitHub repository create/update and, optionally, a GitHub Release.

Preferred workflow:

```text
Project folder / ZIP
        ↓
GitMake prepare
        ↓
Review plan
        ↓
Human approval
        ↓
GitMake apply
        ↓
GitHub
```

GitMake owns the publishing workflow. Do not bypass it with raw `git`, `gh`, or direct GitHub API calls when GitMake can perform the requested operation.

---

## Core rule for LLMs

When the user says something like:

- "Upload this to GitHub"
- "Publish this project"
- "Make a new GitHub repo"
- "Update this repo"
- "Create a release"
- "Prepare this ZIP for GitHub"

**Use GitMake first.**

If GitMake MCP tools are available, prefer them over shell commands.

For normal publishing preparation, use the highest-level tool first:

```text
gitmake_prepare
```

Do not manually compose lower-level GitMake tools unless `gitmake_prepare` cannot finish the task or explicitly reports that more input is required.

---

## Preferred MCP workflow

### 1. Prepare

Call:

```text
gitmake_prepare
```

GitMake should determine as much as possible automatically, including:

- folder vs ZIP source
- repository name
- CREATE vs UPDATE
- branch
- visibility
- source snapshot
- managed sync state
- secret scan
- project identity
- deletion ratio
- destructive risk
- release settings
- reviewed plan

After prepare, show the user these important plan fields:

```text
Repository
Mode
Visibility
Branch
Source
Changes
Risk
Destructive
Release
Plan ID
```

Do not claim that a repository already exists if the plan only says `CREATE`.
Say **"will be created"** or **"planned create"** until apply succeeds.

### 2. Wait for human approval

Do not apply immediately.

Tell the user to approve the reviewed plan with:

```bash
gitmake approve
```

GitMake v1 uses **tokenless approval**.

The user does not need to copy an approval token into the chat.

Approval is stored locally as a short-lived, one-shot grant bound to the exact reviewed plan.

For destructive operations, GitMake may require:

```bash
gitmake approve --destructive
```

and a stronger confirmation string.

Never create, fake, or simulate human approval yourself.

### 3. Apply

After the user confirms that approval is complete, call:

```text
gitmake_apply
```

with the reviewed `plan_id`.

GitMake verifies that the local approval grant matches the exact plan before mutating GitHub.

After apply, report the real result:

```text
Repository
Branch
Push result
Changes
Release
Security result
Duration
Repository URL
Release URL (if any)
```

Do not retry a consumed approval grant as if it were reusable.

---

## Folder mode

GitMake supports publishing the current project folder directly.

Example:

```text
MyProject/
├─ README.md
├─ src/
├─ tests/
├─ pyproject.toml
└─ ...
```

The user can simply say:

> Upload this project to GitHub.

GitMake can snapshot the folder and route it through the same reviewed publishing pipeline used for ZIP files.

Do not create a ZIP manually unless GitMake specifically requires one.

GitMake may use:

```text
.gitignore
.gitmakeignore
```

to exclude files from folder snapshots.

---

## ZIP mode

GitMake also supports project ZIPs.

Example:

```text
Project_v1.0.0.zip
```

If only one plausible project ZIP exists, GitMake may select it automatically.

If multiple plausible sources exist, do not guess. Respect GitMake's ambiguity result and ask the user which source is intended.

For nested publish bundles, distinguish between:

- the actual source ZIP
- release assets
- documentation
- GitMake config

Never assume an outer bundle ZIP itself is the source project if GitMake reports ambiguity.

---

## Zero-config behavior

A `gitmake.json` file is **not required** for normal use.

GitMake can infer a safe configuration in memory.

Do not create or edit `gitmake.json` with generic filesystem Write/Edit tools unless the user explicitly wants a persistent config and GitMake does not provide a config-writing tool.

If GitMake config tools are available, use them instead.

Safe defaults may include:

```text
branch      main
visibility  private for new repos unless explicitly requested otherwise
sync        managed
security    secret scan enabled
```

Always show inferred values to the user in the plan before apply.

---

## Project memory

After a successful publish, GitMake may store project identity in:

```text
.gitmake/project.json
```

This allows GitMake to remember the repository target even if the local folder is renamed.

Do not manually rewrite project identity files.

If GitMake reports a project identity mismatch, stop and ask the user to review it.

---

## Managed sync

GitMake uses managed synchronization to reduce accidental deletion of remote-only files.

Important behavior:

- Files previously managed by GitMake may be updated or removed.
- Remote-only files that GitMake never managed should normally be preserved.
- Protected paths may be preserved.
- Large deletion ratios may be classified as destructive.

Do not bypass a destructive-change block.

If GitMake reports that many managed files would be deleted, show that warning clearly to the user.

---

## Security rules

GitMake may block publishing when it detects:

- `.env` or credentials
- private keys
- tokens
- suspicious secrets
- oversized GitHub files
- Git LFS candidates
- branch protection conflicts
- stale plans
- project identity mismatches
- source/config changes after review
- destructive mass deletion
- concurrent apply attempts

If blocked, do not work around the safety gate with raw `git`, `gh`, GitHub API calls, force pushes, or manual repository mutations.

Explain the reason and follow GitMake's recommended recovery path.

---

## Plan integrity

A GitMake plan is reviewed state, not a suggestion that can be silently changed.

A plan may be bound to:

- source hash
- config hash
- repository
- branch
- remote baseline
- project identity
- plan fingerprint

If the source, config, or remote repository changes after plan creation, GitMake may reject the plan as stale.

When this happens:

1. create a fresh plan
2. show the new changes to the user
3. obtain fresh human approval
4. apply only the new plan

Never reuse an old approval for a new plan.

---

## Release behavior

If release publishing is configured, GitMake may create a GitHub Release after the repository push.

A release can contain:

```text
tag
title
release notes
assets
draft/prerelease state
```

Do not invent release assets.

Use only files selected or validated by GitMake.

If repository publishing succeeds but a release step fails, prefer GitMake's recovery/resume behavior rather than manually uploading assets with another tool.

---

## CLI usage

For humans, the normal CLI is intentionally small.

### Publish current project

```bash
gitmake
```

### Approve latest pending reviewed plan

```bash
gitmake approve
```

### Approve a destructive reviewed plan

```bash
gitmake approve --destructive
```

### Upgrade GitMake

```bash
gitmake upgrade
```

### Check version

```bash
gitmake --version
```

### Check environment

```bash
gitmake doctor
```

### Connect Claude Code / MCP

Read-only:

```bash
gitmake ai setup
```

Write-capable GitMake MCP:

```bash
gitmake ai setup --write
```

Check connection:

```bash
gitmake ai status
```

Expert commands exist, but normal users should not need to memorize them.

---

## MCP availability

If GitMake MCP is available:

**Prefer MCP.**

Normal AI publishing flow:

```text
gitmake_prepare
→ show plan
→ wait for user to run `gitmake approve`
→ gitmake_apply(plan_id)
→ report result
```

If `gitmake_apply` is missing, the MCP connection may be read-only, stale, or connected to an older GitMake version.

Do not bypass GitMake.

Ask the user to check:

```bash
gitmake --version
gitmake ai setup --write
gitmake ai status
```

and restart the LLM client if its MCP tool list is stale.

---

## Things an LLM must NOT do

Do not:

```text
- force push
- rewrite Git history
- delete repositories
- bypass GitMake approval
- create fake approval
- silently choose an ambiguous source
- manually mutate GitHub when GitMake blocks an operation
- claim CREATE already happened before apply
- reuse consumed approval
- ignore PLAN_STALE
- ignore project identity mismatch
- bypass secret detection
- bypass destructive-change protection
- directly edit GitMake internal state files
```

Do not use raw `git`, `gh`, or GitHub API calls for publishing when the user explicitly asked to use GitMake or GitMake can perform the task.

---

## Recommended LLM response style

Keep GitMake interactions short.

Good:

```text
GitMake prepared the project.

Repository  owner/MyProject
Mode        CREATE
Visibility  private
Changes     +12 ~0 -0
Risk        low
Plan        gm_...

Run `gitmake approve` to approve this reviewed plan.
```

After approval and apply:

```text
Published successfully.

Repository  owner/MyProject
Branch      main
Changes     +12 ~0 -0
Release     none

https://github.com/owner/MyProject
```

Avoid dumping internal implementation details unless the user asks.

---

## If you are unsure

Ask GitMake first.

Prefer:

```text
GitMake → inspect / prepare / validate / plan
```

over guessing.

The product philosophy is:

> **Automate the repetitive GitHub publishing work, but keep risky mutations reviewable and human-approved.**
