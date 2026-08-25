# JJP v1.1.0 — GitMake publish intent

This file is project-specific guidance for an LLM preparing this archive with GitMake.
Read `GITMAKE_FOR_LLM.md` first and follow its approval rules.

## Intended operation

- Project: JJP
- Version: `1.1.0`
- Repository operation: UPDATE the existing JJP repository when GitMake project identity / remote baseline confirms it
- Visibility: public
- Branch: preserve the repository's configured/default branch (normally `main`)
- Sync: managed
- Security scan: enabled
- Destructive changes: not expected; stop and show the plan if GitMake reports destructive risk

Do not guess a repository target if GitMake cannot resolve the existing JJP repository safely.

## GitHub Release

Create a normal, non-draft, non-prerelease GitHub Release after the repository update succeeds.

- Tag: `v1.1.0`
- Title: `JJP v1.1.0 — Operational UX`
- Release notes source: `RELEASE_NOTES.md`
- Assets:
  - `dist/jjp-windows-amd64.exe`
  - `dist/jjp-linux-amd64`
  - `dist/jjp-linux-arm64`
  - `dist/SHA256SUMS.txt`

The `dist/` files are release assets, not source files that should be committed to repository history.

## Required flow

1. Run `gitmake_prepare` using this project as the source.
2. Show Repository / Mode / Visibility / Branch / Changes / Risk / Destructive / Release / Plan ID.
3. Do not apply yet.
4. Wait for the human to run `gitmake approve` (or the stronger destructive approval if GitMake requires it).
5. Only after the human confirms approval, run `gitmake_apply` with the exact reviewed plan ID.
