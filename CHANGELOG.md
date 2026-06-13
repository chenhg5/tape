# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
