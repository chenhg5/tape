# Tape

> Record, search and replay your AI coding sessions.
> Nothing gets lost on tape.

You spend hours (and dollars) talking to Claude Code, Codex and Cursor. Those conversations are project knowledge — decisions, rejected approaches, the *why* behind the code. But they're scattered across vendor-specific formats, locked to one machine, and impossible to search.

Tape treats your sessions as **data you own**:

```bash
tape sync                      # archive sessions from all agents into ~/.tape
tape search "为什么不用 OAuth2"  # full-text search across every agent, CJK included
tape show <id>                 # replay any archived session
```

## Supported agents

| Agent | Storage parsed |
|---|---|
| Claude Code | `~/.claude/projects/*/*.jsonl` |
| Codex CLI | `~/.codex/sessions/**/rollout-*.jsonl` |
| Cursor CLI | `~/.cursor/chats/*/*/store.db` |

More sources are pluggable — each agent is a small adapter behind one interface.

## Install

```bash
go install github.com/chenhg5/tape/cmd/tape@latest
```

## Usage

```bash
tape sync                     # incremental: only new/changed sessions are archived
tape ls --project .           # sessions of the current project, across all agents
tape ls --agent codex
tape search "sandbox" --since 30d --limit 10
tape show codex/019ea0af      # id prefixes work everywhere
tape show <id> --full         # include tool outputs
```

### Search that actually works for Chinese

Most session-search tools tokenize for English only. Tape tokenizes latin text by word (prefix matching) and CJK text by character bigrams — `tape search "会话备份"` just works, with BM25 ranking on top.

### Built for agents, too

Every command speaks JSON with a stable schema:

```bash
tape search "auth refactor" --json --limit 5
tape ls --project . --json
```

Exit codes are semantic: `0` ok, `1` error, `2` usage, `3` no results.

## How it stores your data

```
~/.tape/
├── archive/<agent>/<project>/<session-id>/
│   ├── raw/          # byte-for-byte copy of the original files (source of truth)
│   ├── session.json  # normalized, human-readable, greppable
│   └── meta.json     # checksums, provenance
└── index/tape.db     # SQLite FTS5 — derived, always rebuildable
```

Raw files are first-class citizens: summaries and indexes can always be regenerated; originals can't. The archive is plain files — `git init ~/.tape/archive` and push it wherever you trust.

## Roadmap

- [x] **Archive & search** — claude-code, codex, cursor
- [ ] **Backup** — git / S3 / tarball targets, with secret redaction
- [ ] **Restore** — continue a Claude Code session in Codex (and vice versa)
- [ ] **Memory** — distill MEMORY.md from your sessions, MCP server for agents

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) (中文) for the full design.

## License

MIT
