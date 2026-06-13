---
name: tape
description: >-
  Archive, search and restore AI coding sessions (Claude Code, Codex, Cursor,
  OpenCode, Gemini CLI, Antigravity CLI, Qwen Code, iFlow, Qoder, MiMo Code, Kimi Code, Aider) with
  the tape CLI. Use when the user wants to find a past conversation, continue
  a session in a different agent, back up session history, rescue history
  from an EOL'd agent (iFlow, sunset Gemini CLI), or when context from an
  earlier coding session would help the current task.
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
tape sync --full                   # re-archive everything (after a parser bump)

tape ls --host local               # hide remote-mirrored sessions
tape ls --host user@host           # only sessions from that remote
tape search "auth" --host user@host
```

Picker rows from remote hosts are tagged with a dim `@host` badge, and the
**Resume** action on a remote row execs `ssh <host> -t 'cd <cwd> && <agent>
--resume <id>'` automatically — you land inside the conversation on the
right machine without copy-pasting anything.

**Find past context** (CJK queries fully supported):

```bash
tape search "jwt refactor" --limit 5
tape search "为什么不用 oauth2" --dir . --since 30d --agent claude-code
tape search codex                  # also matches by agent / title / project
tape search "memory leak" --sort relevance   # classic BM25 ranking
```

Default sort is `recent` — newest session first, capped at 5 hits per
session so one long conversation can't push fresher matches off the
page. Use `--sort relevance` for classic BM25 ranking when mining old
archives. Each hit has `session_id`, `snippet`, `title`, `project`,
`timestamp`. Every session also has a synthetic `@meta` row in the index
so agent names, session titles and project names are searchable too.
Every session has a synthetic `@meta` row in the index, so agent names,
session titles and project names are searchable too — useful for `"all my
codex sessions about auth"` style queries. Combine with `--agent` /
`--dir` for precise filtering. `--dir <path>` (use `.` for the current
directory) narrows by the session's working directory.

**Paginate** large result sets — both `ls` and `search` accept `--limit N
--page P` (1-based) and return `page`, `page_size`, `has_more`, `total`
(ls only) in JSON. Iterate until `has_more` is `false`.

**Read a session**:

```bash
tape ls --dir . --since 7d         # list; ids accept any unique fragment
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

**Agents must always pass `<session-id>` and `--to`.** Run with no args
*only* on a real TTY; in that case tape opens a numbered picker (session
→ target agent → strategy). Piped or `--json` invocations always demand
both args and exit 2 otherwise, so scripts stay deterministic.

Strategies, highest fidelity first:

| Strategy     | What it does                                                                                              | Best for                              |
|--------------|-----------------------------------------------------------------------------------------------------------|---------------------------------------|
| `native`     | Rewrites as a real session of the target agent; returns a `resume_command`. claude-code ↔ codex only.     | Same-agent-family resume              |
| `memory`     | Writes a full transcript and `@`-references it from the target's project memory file (`CLAUDE.md` for claude-code; `AGENTS.md` for codex/cursor/opencode/qoder/kimi-code; `GEMINI.md` for gemini/antigravity; `MEMORY.md` for mimocode; `QWEN.md` / `IFLOW.md` / `CONVENTIONS.md` for the rest) so the agent auto-loads it. | Cross-agent resume that "just works"  |
| `transcript` | Writes the full verbatim conversation as markdown; user/agent reads on demand.                            | When you don't want to touch memory files |
| `brief`      | LLM-condensed handoff. `--llm none` falls back to a deterministic template.                               | Token-constrained handoffs            |

`auto` (default) picks `native` if the target supports it, otherwise
`memory`. For agents the recommended call is explicit, e.g.
`tape restore @last --to cursor --strategy memory --json`.

**Export** the archive (or a filtered slice) as one file — or split
across several. Tape only *generates* the snapshot — uploading to
S3/git/Drive is the caller's job, by design:

```bash
tape export                                    # tape-export-<ts>.tar.zst in cwd
tape export snapshot.tar.gz --compress gzip    # explicit name + codec
tape export --agent codex --since 7d           # filter-scoped slice
tape export --exclude-agent cursor,opencode    # everything except those two
tape export --exclude-host local               # only remote-mirrored sessions
tape export --dir . --format zip --compress none  # current project as a .zip
tape export --split-by agent                   # one file per agent
tape export --split-by month --since 1y        # one file per calendar month
tape export --split-by size --split-size 200M  # 200 MiB buckets
tape export --split-by agent --jobs 4          # 4 chunks at a time
tape export --scan-only                        # audit secrets, write nothing
```

`--format tar|zip` × `--compress zstd|gzip|xz|none` (zip implies its own
DEFLATE so `--compress zstd` etc. is a usage error). Default is tar+zstd.
For large archives use `--split-by none|size|agent|month` to fan out;
the chunk discriminator (agent name / `YYYY-MM` / `part-001` …) is
inserted before the extension and every chunk is listed under `parts[]`
in the JSON envelope.

`--jobs N` controls parallelism for chunked exports: chunks run
concurrently (capped at `min(N, chunk-count)`) and the remaining
budget feeds each chunk's zstd encoder, so total threads stay ≈ N.
`--jobs 0` (default) is `GOMAXPROCS`; `--jobs 1` is the
reproducible single-thread baseline. JSON output adds
`split.jobs` + `split.encoder_concurrency` so agents can verify the
actual run shape.

Every filter flag has an `--exclude-*` counterpart on `ls` / `search`
/ `export`: `--exclude-agent`, `--exclude-dir`, `--exclude-host`.
Each is repeatable (`--exclude-agent cc --exclude-agent oc`) or
comma-separated (`--exclude-agent cc,oc`); the same agent-name
normalization (canonical + shorthand + did-you-mean) applies.
`--exclude-host local` is the natural "remote-only" idiom; positive
and negative on the same axis layer as AND-NOT
(`--agent codex --exclude-dir /tmp`).
Secrets in `session.json` are redacted in stream
(`[REDACTED:<rule>]` for text, length-preserving masks for binaries);
pass `--no-redact` for verbatim bytes. `--scan-only` is exit 0 even when
findings exist — it's an audit mode, not a gate (the redactor is the
gate, and it always runs on real exports).

**Keep the archive fresh** — `tape sync` is idempotent and cheap, but
the user has to remember to run it. Three options, lowest to highest
effort:

```bash
tape sync                                # ad-hoc, on demand
tape sync --install --interval 1h        # systemd timer / LaunchAgent / schtasks
tape sync --status                       # is one installed?
tape sync --uninstall                    # remove it
```

`--install` writes a user-scoped job — systemd user timer on Linux
(`~/.config/systemd/user/tape-sync.timer`), LaunchAgent on macOS
(`~/Library/LaunchAgents/com.tapeai.sync.plist`), or a printed
`schtasks /Create` command on Windows. The job runs as the user,
never root. Repeat `--remote user@host` flags are preserved in the
unit file so scheduled syncs cover remote machines too.

**Versioning & updates** — no background pings; checks are explicit:

```bash
tape version                       # version, commit, build date, install method
tape update --check                # any newer release? exit non-zero if so
tape update                        # upgrade in place via the matching installer
tape update --channel beta --dry-run
```

`tape update` reads the install method from `tape version` and runs
the right thing: `npm install -g @tapeai/tape@<tag>` for npm,
`go install github.com/chenhg5/tape/cmd/tape@<tag>` for `go install`.
Homebrew / manual installs get the suggested command printed but
nothing is executed.

## Conventions

- Session ids look like `codex/019ea0af-...`; any unique fragment resolves,
  `@last` means the most recent session.
- `--agent` / `--to` accept full names, the obvious aliases
  (`claude`/`kimi`/`mimo`), and a 2-letter shorthand:
  `cc` claude-code, `cx` codex, `cu` cursor, `oc` opencode, `gm` gemini,
  `ag` antigravity, `qw` qwen, `qd` qoder, `if` iflow, `ad` aider,
  `mi` mimocode, `kc` kimi-code. Case and separators are ignored
  (`Claude_Code` works); unknown names produce a usage error with a
  "did you mean…" hint. Agents should still pass the canonical
  spelling — the shorthand exists for humans typing in a terminal.
- `--dry-run` first for anything that writes (restore, export).
- All timestamps are RFC 3339; `--since` accepts `24h`, `7d`, `2026-01-31`.
- The archive lives in `$TAPE_HOME` (default `~/.tape`; legacy `$TAPE_DIR`
  is still honored). There is no `--dir` for tape's storage location on
  purpose: `--dir` on `ls`/`search`/`restore` filters by the *project*
  directory, not tape's data dir.
- Subcommand names accept any unambiguous prefix (`tape sy` → sync,
  `tape sho` → show, `tape re` → restore). For agents, always spell out
  the full name to stay forward-compatible if new commands are added.
