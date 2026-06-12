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
tape search "sandbox" --since 30d --limit 10
tape show codex/019ea0af      # id prefixes work everywhere; --full includes tool output
```

### Continue a session in another agent

```bash
tape restore claude-code/04ebf6a4 --to codex
# → restored as a native codex session. Resume it with:
#     cd /root/code/demo && codex resume 019ebb66-899f-77cf-b66c-9009d4c5f09b
```

Two strategies, picked automatically:

- **native** — rewrites the dialogue as a real session of the target agent, resumed with the agent's own `--resume`. Supported for claude-code ↔ codex.
- **brief** — generates a handoff document (`tape restore <id> --to cursor`), summarized by whichever agent CLI you already have installed (`--llm claude|codex|cursor|none`); falls back to a deterministic template.

### Back up everything

```bash
tape backup push --remote git@github.com:you/tape-archive.git   # archive-as-git-repo
tape backup pull --remote ...                                   # fresh machine: clone + reindex
tape backup export --output tape-archive.tar.zst                # redacted compressed snapshot
tape backup scan                                                # find secrets before they leak
```

`backup push` refuses to push when the secret scan finds anything (real keys
end up in sessions more often than you think — scan yours), unless you pass
`--allow-secrets`. `backup export` replaces secrets with `[REDACTED:<rule>]`
inside the artifact; your local files are never modified.

### Search that actually works for Chinese

Most session-search tools tokenize for English only. Tape tokenizes latin text by word (prefix matching) and CJK text by character bigrams — `tape search "会话备份"` just works, with BM25 ranking on top.

### Built for agents, too

```bash
tape search "auth refactor" --json --limit 5   # stable JSON schema
tape schema backup push                        # introspect commands as JSON
```

- JSON is the default whenever stdout is not a TTY; colors honor `NO_COLOR`
- Semantic exit codes: `0` ok, `1` error, `2` usage, `3` no results, `10` dry-run passed
- Every destructive or stateful command supports `--dry-run`
- Errors are machine-readable: `{"error":"secrets_found","suggestion":"...","retryable":false}`

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
- [x] **Backup** — git and tarball targets, secret scanning and redaction
- [x] **Restore** — continue a Claude Code session in Codex (and vice versa)
- [ ] **Memory** — distill MEMORY.md from your sessions, MCP server for agents
- [ ] More sources (Gemini CLI, OpenCode, Aider) and backup targets (S3)

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) (中文) for the full design.

## License

MIT
