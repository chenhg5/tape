---
name: tape
description: >-
  Archive, search and restore AI coding sessions (Claude Code, Codex, Cursor)
  with the tape CLI. Use when the user wants to find a past conversation,
  continue a session in a different agent, back up session history, or when
  context from an earlier coding session would help the current task.
---

# Using tape

Tape archives coding-agent sessions into a local store the user owns, makes
them searchable, and can replay them into another agent. Run `tape` like any
shell command.

## Output contract

- stdout is one JSON object `{"schema_version":1,"data":{...}}` whenever it
  is piped (always the case for you). Parse `data`, ignore unknown fields.
- Errors arrive on stderr as JSON: `{"error":"<type>","message":"...","suggestion":"...","retryable":bool}`.
- Exit codes: `0` ok · `1` error · `2` usage error · `3` no results · `10` dry-run passed.
  Branch on exit codes, not output text. Exit 3 still emits valid data with
  `count: 0` — empty is not a failure.
- `tape schema [command...]` returns the full command/flag tree as JSON.
  Prefer it over `--help` parsing.

## Core workflows

**Refresh the archive first** (cheap and idempotent — checksums skip
unchanged sessions):

```bash
tape sync                          # local agents
tape sync --remote user@host       # also pull from an SSH machine
```

**Find past context** (CJK queries fully supported):

```bash
tape search "jwt refactor" --limit 5
tape search "为什么不用 oauth2" --project . --since 30d --agent claude-code
```

Each hit has `session_id`, `snippet`, `title`, `project`, `timestamp`.

**Read a session**:

```bash
tape ls --project . --since 7d     # list; ids accept any unique fragment
tape show <session-id>             # full session with messages
tape overview                      # archive-wide stats
```

**Continue a session in another agent** (the user hit a token limit, or
wants to switch tools):

```bash
tape restore <session-id> --to codex --dry-run   # preview plan, exit 10 = ok
tape restore <session-id> --to codex             # returns resume_command
tape restore @last --to cursor --strategy brief --llm none   # handoff file
```

Native restore (claude-code ↔ codex) returns a `resume_command` to run.
Brief restore writes a handoff markdown file and returns a `start_command`;
`--llm none` is deterministic (no subprocess LLM call).

**Back up** (push is blocked if the secret scan finds anything):

```bash
tape backup scan                   # exit 0 = clean, exit 1 = secrets found
tape backup push --remote <git-url>
tape backup export --output archive.tar.zst   # redacted snapshot
```

## Conventions

- Session ids look like `codex/019ea0af-...`; any unique fragment resolves,
  `@last` means the most recent session.
- `--dry-run` first for anything that writes (restore, backup push/export).
- All timestamps are RFC 3339; `--since` accepts `24h`, `7d`, `2026-01-31`.
- The archive lives in `$TAPE_DIR` (default `~/.tape`); pass `--dir` to use
  another location without touching the environment.
