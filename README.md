<div align="center">

# tape

**Record, search and replay your AI coding sessions.**

*Nothing gets lost on tape.*

[![CI](https://github.com/chenhg5/tape/actions/workflows/ci.yml/badge.svg)](https://github.com/chenhg5/tape/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/chenhg5/tape.svg)](https://pkg.go.dev/github.com/chenhg5/tape)
[![Go Report Card](https://goreportcard.com/badge/github.com/chenhg5/tape)](https://goreportcard.com/report/github.com/chenhg5/tape)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

</div>

---

You spend hours (and dollars) talking to **Claude Code**, **Codex** and **Cursor**. Those conversations are project knowledge — decisions made, approaches rejected, the *why* behind every line of code. But they are scattered across vendor-specific formats, locked to one machine, and impossible to search.

Tape turns them into **data you own**:

```console
$ tape sync
claude-code  scanned 124  archived 3   skipped 121
codex        scanned 87   archived 1   skipped 86
cursor       scanned 45   archived 2   skipped 43
synced: 6 session(s) archived

$ tape search "为什么不用 OAuth2"
claude-code/7dd2afaf  Auth migration        …为什么不用 OAuth2 而是 JWT?…

$ tape restore claude-code/7dd2afaf --to codex
restored claude-code/7dd2afaf as a native codex session.
Resume it with:

  cd /root/code/demo && codex resume 019ebb66-899f-77cf-b66c-9009d4c5f09b
```

## Highlights

- **Own your history** — every session is archived as plain files under `~/.tape`, byte-for-byte raw copies included. No database lock-in, no cloud.
- **All your machines** — `tape sync --remote user@host` pulls sessions from dev servers over plain SSH; nothing to install on the remote side.
- **Search everything** — full-text search across all agents with BM25 ranking. CJK works: latin words *and* Chinese/Japanese/Korean bigrams are tokenized natively.
- **Move between agents** — ran out of Claude tokens mid-task? `tape restore --to codex` rewrites the dialogue as a *native* session the target agent can `resume`.
- **Back up safely** — `backup push` turns the archive into a git repo; a built-in secret scanner blocks pushes containing API keys. `backup export` produces a redacted `.tar.zst`.
- **Built for agents, too** — JSON output when piped, semantic exit codes, machine-readable errors, `--dry-run` everywhere, and `tape schema` for command introspection.
- **Single binary** — pure Go, no CGO, no runtime dependencies. Linux / macOS / Windows.

## Table of contents

- [Installation](#installation)
- [Quick start](#quick-start)
- [Commands](#commands)
- [Searching](#searching)
- [Restoring sessions across agents](#restoring-sessions-across-agents)
- [Backing up](#backing-up)
- [Agent &amp; script mode](#agent--script-mode)
- [How tape stores your data](#how-tape-stores-your-data)
- [Supported agents](#supported-agents)
- [Development](#development)
- [Roadmap](#roadmap)
- [License](#license)

## Installation

With npm (prebuilt binaries, no Go required):

```bash
npm install -g agent-tape          # stable
npm install -g agent-tape@beta     # beta channel
```

With Go 1.26+:

```bash
go install github.com/chenhg5/tape/cmd/tape@latest
```

From source:

```bash
git clone https://github.com/chenhg5/tape && cd tape && make build
```

## Quick start

```bash
tape sync          # 1. archive sessions from every installed agent (incremental)
tape ls            # 2. see what you have
tape search "..."  # 3. find that conversation from three weeks ago
tape show <id>     # 4. replay it
```

`tape sync` is safe to run any time — content checksums (blake3) make it incremental and idempotent. Add it to cron if you like.

### Sync from other machines

Sessions living on a dev server or a second laptop are one flag away:

```bash
tape sync --remote dev@build-server --remote user@10.0.0.7
```

Tape mirrors the agent directories over plain `ssh` + `tar` (nothing to install on the remote side, respects your `~/.ssh/config`), then archives them locally. Pulls are incremental after the first one, and remote sessions carry a `host` field so you always know where a conversation happened.

## Commands

| Command | What it does |
|---|---|
| `tape sync` | Archive new/changed sessions from all agents (`--remote user@host` for SSH machines) |
| `tape ls` | List archived sessions (`--agent`, `--project .`, `--since 7d`) |
| `tape search <query>` | Full-text search across everything |
| `tape show <id>` | Replay a session (`--full` includes tool output) |
| `tape restore <id> --to <agent>` | Continue a session in another agent |
| `tape backup push / pull` | Sync the archive with a private git remote |
| `tape backup export / scan` | Redacted tarball snapshot / standalone secret scan |
| `tape index rebuild` | Regenerate the search index from the archive |
| `tape schema [command]` | Introspect the CLI as JSON (for agents) |

Session ids never need to be typed in full — any unique fragment resolves (`tape show 7dd2afaf`), and `@last` refers to the most recent session.

## Searching

```bash
tape search "race condition" --agent codex --since 30d --limit 10
tape search "会话备份"          # CJK queries just work
tape search "auth" --project . # only this project's sessions
```

Most session-search tools tokenize for English only and silently fail on CJK text. Tape tokenizes latin text by word (with prefix matching) and CJK text by overlapping character bigrams, indexed in SQLite FTS5 with BM25 ranking. Search hits show the matching snippet, not just the session.

## Restoring sessions across agents

The problem tape was born for: you've been working with one agent, hit a token/quota wall, and want to continue in another — *with* the full conversation.

```bash
tape restore claude-code/7dd2afaf --to codex      # native: codex resume <id>
tape restore codex/019ea0af --to claude-code      # native: claude --resume <id>
tape restore codex/019ea0af --to cursor           # brief: handoff document
tape restore @last --to codex --dry-run           # preview the plan first
```

Two strategies, chosen automatically:

- **native** — rewrites the dialogue as a real session file of the target agent, which then resumes it with its own `--resume` mechanism. Supported for claude-code ↔ codex.
- **brief** — generates a structured handoff document (goal, state, decisions, next steps) and prints the command to start the next agent with it. The summary is written by whichever agent CLI you already have installed (`--llm claude|codex|cursor`), with a deterministic template fallback (`--llm none`) — no API keys needed.

## Backing up

```bash
tape backup push --remote git@github.com:you/tape-archive.git  # archive-as-git-repo
tape backup pull --remote ...        # fresh machine: clone + rebuild index
tape backup export --output a.tar.zst # compressed, redacted snapshot
tape backup scan                     # what would leak if I pushed this?
```

Real API keys end up in coding sessions more often than you think — scan yours. The secret scanner (AWS, GitHub, OpenAI, Anthropic, Slack, JWT, private keys, …) runs before every push and **blocks on findings** unless you pass `--allow-secrets`. `export` replaces secrets with `[REDACTED:<rule>]` inside the artifact; your local files are never modified.

## Agent & script mode

Tape follows the [agent-cli-guide](https://github.com/Johnixr/agent-cli-guide) conventions throughout, so other AI agents (and your scripts) can drive it reliably:

```console
$ tape search "auth refactor" --limit 5 | jq .data.hits[0].session_id
"claude-code/7dd2afaf"

$ tape schema backup push   # what flags does this command take?
```

- **JSON by default when piped** — every command emits one `{"schema_version":1,"data":{...}}` object on stdout when it is not a TTY (or with `--json`). Colors honor `NO_COLOR`.
- **Semantic exit codes** — `0` ok · `1` error · `2` usage · `3` no results · `10` dry-run passed. Agents branch on codes, not on prose.
- **Machine-readable errors** — `{"error":"secrets_found","message":"...","suggestion":"...","retryable":false}` on stderr.
- **`--dry-run` everywhere** state is touched; exit 10 means "safe to run for real".

## How tape stores your data

```
~/.tape/
├── archive/<agent>/<project>/<session-id>/
│   ├── raw/          # byte-for-byte copy of the original files (source of truth)
│   ├── session.json  # normalized, human-readable, greppable
│   └── meta.json     # checksums, provenance
└── index/tape.db     # SQLite FTS5 — derived, always rebuildable
```

The design rule: **raw files are first-class, everything else is derived.** Summaries, indexes and handoffs can always be regenerated; the original conversation cannot. The archive is plain files — readable without tape, diffable in git, owned by you.

## Supported agents

| Agent | Reads | Native restore target |
|---|---|---|
| Claude Code | `~/.claude/projects/*/*.jsonl` | yes |
| Codex CLI | `~/.codex/sessions/**/rollout-*.jsonl` | yes |
| Cursor CLI | `~/.cursor/chats/*/*/store.db` | brief handoff |

Each agent is a small adapter behind one interface ([`ports.Source`](internal/core/ports/ports.go)); adding a new one does not touch the core. Contributions for Gemini CLI, OpenCode and Aider are welcome.

## Development

```bash
make ci      # everything CI runs: fmt, vet, race tests, coverage gate, smoke, e2e
make smoke   # fast sanity subset (~1s)
make e2e     # full end-to-end suite against the real binary
```

The test pyramid: unit tests per package, fixture tests for every session format parser, and an end-to-end suite that builds the actual binary and drives it against fake `$HOME` data — exit codes, JSON contracts, secret gates and disaster-recovery drills included. CI enforces formatting, `go vet`, the race detector, a coverage floor and cross-compilation for five platforms on every push.

Architecture deep-dive (中文): [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)

## Roadmap

- [x] **Archive & search** — claude-code, codex, cursor; CJK tokenization
- [x] **Backup** — git and tarball targets, secret scanning and redaction
- [x] **Restore** — native claude-code ↔ codex, brief handoff for everything else
- [ ] **Memory** — distill `MEMORY.md` from session history; MCP server so agents can search past sessions mid-task
- [ ] More sources (Gemini CLI, OpenCode, Aider) and backup targets (S3/OSS)

## License

[MIT](LICENSE)
