# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.0] — 2026-06-15

Focused follow-up to v0.1.0: fixes the SQL crash hit by users who
upgraded an older index, rewires the npm distribution to a single
package, and makes empty-state errors actionable.

### Fixed

- **`tape search` no longer crashes with `SQL logic error: no such
  column: s.host (1)`** after upgrading from a pre-v4 index. The
  `host` column added in 0.1.0's v4 schema is now applied via an
  idempotent `ALTER TABLE` migration the first time `Open()` runs
  against a legacy DB, instead of silently relying on a re-create
  that never fired. Existing rows get `host=NULL` until the next
  `tape sync` re-Upserts them; the `COALESCE(s.host,'')` already in
  every query handles the transitional state without surprises.
- **Picker / table alignment for long agent names** (`claude-code`,
  `antigravity`). The shortID column was hard-coded to 22 cells; the
  worst case is 23 (`claude-code/` + abbreviated id). Every row from
  one of those agents pushed agent / time / snippet right by one
  cell. Fixed by raising the constant to 24 and consolidating five
  duplicated literals behind a single `shortIDColW`. Regression test
  iterates `agentid.Canonicals()` so a future 12-rune agent fails
  loudly instead of reintroducing the bug.

### Changed

- **npm: one package instead of six.** `@tapeai/tape` is now a
  ~15 KB launcher. On install, the postinstall script races GitHub
  and Gitee for the right prebuilt binary, verifies its sha256,
  and drops it in `bin/`. If install-time network is blocked
  (CI without egress, `--ignore-scripts`, locked-down corporate
  proxies), the binary is lazily fetched on the first `tape ...`
  invocation — `npm install` itself never fails just because the
  download did. Pin with `TAPE_MIRROR=github|gitee`.
- **Empty index / archive now self-explain.** A first-time `tape
  search` on a fresh machine used to return a bare `tape: no
  results` with no path forward. It now reports either
  `tape: no archived sessions yet — try: run \`tape sync\`...` (ls)
  or `tape: no results — your search index is empty — try: run
  \`tape sync\`...` (search), and serialises both in `--json` mode
  under `.message` / `.suggestion`. Exit code stays at 3, so any
  agent script branching on "found nothing" keeps working.

### Added

- **Raw binaries in every GitHub + Gitee release.** Each release
  now ships both the bundled `tape_<ver>_<os>_<arch>.{tar.gz,zip}`
  (LICENSE + README + binary) *and* the raw
  `tape-<os>-<arch>[.exe]` with matching `.sha256` sidecars. The
  one-line installer prefers the raw asset (one curl, no tar/unzip
  dep, Docker `COPY`-friendly) and falls back to the bundled
  archive when the raw isn't on the release.
- **Brand assets** (`assets/`): minimal reel-to-reel SVG logo in
  wordmark + mark + favicon variants, with `prefers-color-scheme`
  switching via `<picture>` in the README. Single-color, vector-only,
  16 px favicon-grade rasterization.

### Internal

- `cliError` now satisfies `errors.Is(_, ErrNoResults)` when its
  Type is `"no_results"`, so commands can attach a Suggestion on
  the empty path without losing the exit-code-3 contract.
- `scripts/release-npm.sh` refuses to run from anywhere except the
  repo root, refuses to publish if the matching binary asset is
  missing from the GitHub release, and now distinguishes "stale
  checkout" (pre-rewrite npm/ layout) from "wrong cwd" in its
  guardrail so users know whether to `git pull` or `cd`.

## [0.1.0] — 2026-06-13

First public release. Tape archives, searches, and replays AI coding
sessions across 12 agents, so context survives token-limit resets,
machine moves, and swaps between tools.

### Added

#### Twelve agent sources (`tape sync`)

- **Claude Code** (`cc`) — `~/.claude/projects/<dir>/*.jsonl`
- **Codex** (`cx`) — `~/.codex/sessions/**/*.jsonl`
- **Cursor** (`cs`) — `~/.config/Cursor/agent-sessions/**/*.{json,jsonl}` (Linux),
  `~/Library/Application Support/Cursor/...` (macOS), `%APPDATA%\Cursor\...` (Windows)
- **Gemini CLI** (`ge`) — `~/.gemini/tmp/<project-hash>/*.json` + `logs.json`
- **Antigravity CLI** (`ag`) — `~/.antigravity/...`
- **Qwen Code** (`qw`) — `~/.qwen/tmp/...`
- **iFlow** (`if`) — `~/.iflow/tmp/...`
- **Qoder CLI** (`qo`) — `~/.qoder/...`
- **Aider** (`ai`) — `.aider.chat.history.md` walked under cwd
- **OpenCode** (`oc`) — SQLite + JSONL hybrid
- **MiMo Code** (`mi`) — Xiaomi's MiMo
- **Kimi Code** (`ki`) — Moonshot's Kimi

Each source implements `ports.Source` (Name + Detect + List + Read).
Sources never write back to the agent's own storage.

#### Unified session model

- `model.Session` + `model.Summary` as the canonical IR every source
  parses into and every consumer (`ls`, `search`, `show`, `export`,
  `restore`) reads from.
- Redaction layer (`internal/redact`) strips secrets — tokens, API
  keys, .env values, SSH-key blocks — before anything hits the
  archive or the index.

#### Browse, search, restore

- `tape ls [--agent --dir --host --since]` — interactive picker on
  a TTY, JSON when piped. `--exclude-agent / --exclude-dir /
  --exclude-host` for negative filtering.
- `tape search "<query>" [filters]` — FTS5 across messages, agents,
  projects, file paths.
- `tape show <id>` — render one full session.
- `tape overview` — archive activity dashboard.
- `tape restore <id> --to <agent>` — replay a session into the
  target agent. Strategies: `auto`, `native` (agent-specific
  session format), `memory` (memory file), `transcript` (plain
  Markdown), `brief` (LLM-summarized).

#### Export

- `tape export [--format tar|zip] [--compress zstd|gzip|xz|none]`
  — single-file snapshot of the archive.
- `--split-by size|agent|month` + `--split-size 1G` — chunked
  archives for incremental backup / mirror to cloud storage.
- `--jobs N` — parallel chunk packing with shared zstd encoder
  concurrency budget.
- `--scan` — dry-run a chunk plan and exit 10 without writing.
- `--exclude-agent / --exclude-dir / --exclude-host` filters.

#### Sync, schedule, remote

- `tape sync [--since <duration>] [--full]` — pull new sessions.
- `tape sync --install [--interval 1h]` — register a user-scoped
  periodic sync via the platform-native scheduler (systemd user
  timer on Linux, LaunchAgent on macOS, printed `schtasks`
  command on Windows). No root, no daemon.
- `tape sync --uninstall / --status`.
- `--remote <user@host>` — sync over SSH; the local archive carries
  a `host` tag so `ls --host`/`--exclude-host` can scope.

#### Index

- `tape index [--rebuild]` — build the SQLite FTS5 index used by
  `search`.
- Index lives at `~/.tape/index.db`; rebuilds in place atomically.

#### Versioning, updates, ops

- `tape version` — version, commit, build date, Go toolchain,
  platform, install method (npm / go-install / homebrew / manual).
- `tape update [--check] [--channel beta] [--mirror github|gitee]
  [--dry-run]` — explicit update check + in-place upgrade via the
  detected install method. No background pings, ever.
- `tape history [--limit N] [--op <verb>] [--json]` — recent
  sync / export / restore / update operations, recorded as JSONL
  in `~/.tape/operations.log`. Disable with `TAPE_NO_OPLOG=1`.
- `tape uninstall [--dry-run] [--keep-archive] [--force]` —
  remove tape's local state and unschedule the periodic sync.
  Prints the install-method-specific command for the binary
  itself; never self-deletes.
- `tape config {get,set,unset,list,path}` — persisted preferences
  at `~/.tape/config.json` (`defaults.exclude_agents`,
  `defaults.jobs`, `defaults.format`, `defaults.compress`,
  `defaults.exclude_dirs`, `defaults.exclude_hosts`).
  CLI flags win; for slice keys, config defaults are *merged*
  with CLI input (config is a baseline, flag is an increment).
- `tape completion {bash,zsh,fish,powershell}` — shell completion.
- `tape schema [<command>]` — JSON-schema introspection for agents.
- Global `--debug` / `-v` — verbose diagnostics on stderr (source
  detection paths, SQL filters, scheduler payloads, GitHub /
  Gitee probe latencies).
- Global `--json` — machine-readable output. Stdout-is-a-pipe
  auto-enables it.

#### Two release mirrors

- **GitHub** (`chenhg5/tape`) — canonical, npm + go-install origin.
- **Gitee** (`cg33/tape`) — mainland-China mirror.
- `tape update` and `scripts/install.sh` each race a HEAD probe
  (~1.5s budget) across both and pick the fastest. Pin via
  `--mirror github|gitee` or `TAPE_MIRROR=<name>` env. See
  `docs/RELEASE.md` for the release-time double-push playbook.

#### Distribution

- **npm**: `npm install -g @tapeai/tape` (main package + 5
  platform sub-packages: `@tapeai/tape-{linux,darwin}-{x64,arm64}`
  + `@tapeai/tape-win32-x64`). Each platform sub-package ships
  the prebuilt binary; the main package's `tape.js` launcher
  execs whichever the platform resolved.
- **go install**: `go install github.com/chenhg5/tape/cmd/tape@latest`.
- **scripts/install.sh**: `curl -fsSL .../scripts/install.sh | bash`.
  Detects OS+arch, races the GitHub/Gitee mirror probe, verifies
  sha256 when a sidecar is available, falls back to `~/.local/bin`
  when `/usr/local/bin` is unwritable and sudo is unavailable.
- **scripts/setup-raycast.sh** (macOS) — drops 5 Raycast
  script-commands (sync / ls / search / export / overview).

### Documentation

- README — quickstart, agent matrix, command catalog.
- SKILL.md — agent-facing usage guide.
- docs/ARCHITECTURE.md — hexagonal layering, ports/adapters,
  source plugin contract, design rationale per feature.
- docs/RELEASE.md — release playbook (cut tag → release-binaries.sh
  → GitHub release → Gitee mirror → npm publish).

### Tests

- Unit coverage on every source adapter, archive, FTS index,
  redaction, scheduler, oplog, config, mirror.
- End-to-end coverage of every command surface in `test/e2e/`.
- CI on Ubuntu + macOS with `gofmt`, `go vet`, race-mode unit
  tests, coverage gate, smoke, e2e, e2e binary-coverage.

### Exit codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Generic error |
| 2 | Usage error |
| 3 | No results |
| 4 | Newer release available (only from `tape update --check`) |
| 10 | Dry-run completed successfully |

### Known limitations

- Restore strategies vary by agent capability; `--strategy native`
  is only available for agents that expose a writable session
  format. The remaining agents fall back to `memory` or
  `transcript` automatically under `--strategy auto`.
- Remote sync requires `tape` on both sides (the remote machine
  reads its local agent storage and streams sessions back over
  SSH).

[0.1.0]: https://github.com/chenhg5/tape/releases/tag/v0.1.0
