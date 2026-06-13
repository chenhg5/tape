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

You spend hours (and dollars) talking to **Claude Code**, **Codex**, **Cursor**, **OpenCode**, **Gemini CLI** / **Antigravity**, **Qwen Code**, **iFlow** / **Qoder**, **MiMo Code**, **Kimi Code** and **Aider**. Those conversations are project knowledge — decisions made, approaches rejected, the *why* behind every line of code. But they are scattered across vendor-specific formats, locked to one machine, and **impossible to search after the vendor pulls the plug** (looking at you, iFlow).

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
- **Export, don't lock in** — `tape export` ships the archive (or any filtered slice) as a single artifact in your choice of container/codec (`tar.zst`, `tar.gz`, `tar.xz`, `tar`, or `zip`). Secrets are redacted in stream. What you do with the file — git, S3, USB stick — is up to you.
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
npm install -g @tapeai/tape        # stable
npm install -g @tapeai/tape@beta   # beta channel
# CN-friendly:
npm install -g @tapeai/tape --registry=https://registry.npmmirror.com
```

The npm package is a thin launcher (~15 KB). On install, the
postinstall hook races GitHub vs Gitee, fetches the matching prebuilt
binary (`tape-<os>-<arch>[.exe]`) from the release, verifies its
sha256, and drops it in the package's `bin/`. If install-time network
is blocked, the binary is fetched lazily on the first `tape ...`
invocation instead — the install never fails just because the
download did. Pin a mirror with `TAPE_MIRROR=gitee` to force the
fast path for CN networks.

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
tape ls            # 2. browse interactively — Enter on a row to Resume / Show / Copy
tape search "..."  # 3. find that conversation from three weeks ago (same picker)
tape show <id>     # 4. or replay it by id
```

`tape sync` is safe to run any time — content checksums (blake3) make it incremental and idempotent. Add it to cron if you like.

### Sync from other machines

Sessions living on a dev server or a second laptop are one flag away:

```bash
tape sync --remote dev@build-server --remote user@10.0.0.7
```

Tape mirrors the agent directories over plain `ssh` + `tar` (nothing to install on the remote side, respects your `~/.ssh/config`), then archives them locally. Pulls are incremental after the first one, and remote sessions carry a `host` field so you always know where a conversation happened.

Remote sessions are clearly tagged in `tape ls` / `tape search` with a dim `@host` badge, and you can scope by origin with `--host`:

```bash
tape ls --host local                  # only this machine
tape ls --host dev@build-server       # only that one remote
tape search "auth migration" --host dev@build-server
```

Picking **Resume** on a remote row execs `ssh <host> -t 'cd <cwd> && <agent> --resume <id>'` for you — one keystroke from the picker to inside the conversation on the right machine.

Run `tape sync --full` after a tape upgrade if you want to backfill new IR fields into already-archived sessions; the run is O(all sessions) but every write is content-addressed so nothing is duplicated.

## Commands

| Command | What it does |
|---|---|
| `tape sync` | Archive new/changed sessions from all agents (`--remote user@host` for SSH machines; `--full` to re-archive everything after a parser bump; `--install [--interval 1h]` to register a periodic sync job) |
| `tape ls` | Browse archived sessions (interactive picker on a TTY; `--print` for plain table; filters `--agent`, `--dir .`, `--host local\|<ssh>`, `--since 7d`; reverse with `--exclude-agent`, `--exclude-dir`, `--exclude-host`) |
| `tape search <query>` | Full-text search across everything (interactive picker on a TTY; `--print` for plain list; same `--host` / `--exclude-*` filters) |
| `tape show <id>` | Replay a session (`--full` includes tool output) |
| `tape overview` | Dashboard: agents, activity sparkline, top projects, recent sessions |
| `tape restore <id> --to <agent>` | Continue a session in another agent |
| `tape export [output]` | Snapshot the archive to one file or a fan-out of chunks (`--format tar\|zip`, `--compress zstd\|gzip\|xz\|none`, `--split-by none\|size\|agent\|month`, `--jobs N` for parallel writers, positive + reverse filters `--agent`/`--dir`/`--host`/`--exclude-agent`/`--exclude-dir`/`--exclude-host`, `--since`, `--scan-only` for an audit-only run) |
| `tape version` | Print version, git commit, build date, Go toolchain, platform and detected install method (`--json` for scripts) |
| `tape update` | Check for and (default) install a newer release. Auto-picks the right installer for how tape was installed (`npm` / `go install` / manual). `--check` reports only; `--channel beta` includes prereleases |
| `tape history` | Show recent operations (sync / export / restore / update) recorded to `~/.tape/operations.log`. `--limit N`, `--op <verb>`, `--json` |
| `tape config {get,set,unset,list,path}` | Read or modify persistent CLI defaults in `~/.tape/config.json` (e.g. `defaults.exclude_agents`, `defaults.jobs`, `defaults.format`) |
| `tape completion {bash,zsh,fish,powershell}` | Emit a shell completion script (pipe into the right place — see the command's help for per-shell instructions) |
| `tape uninstall` | Remove tape's local state and unschedule any periodic sync (`--keep-archive` to keep sessions, `--dry-run` to preview, `--force` to skip confirmation). Prints the install-method-specific command to remove the binary itself |
| `tape schema [command]` | Introspect the CLI as JSON (for agents) |

Global flags on every command: `--json` (force machine-readable output; auto-on when stdout is a pipe), `-v` / `--debug` (verbose diagnostics on stderr — source detection paths, scheduler payloads, the GitHub URL `tape update` hits).

Session ids never need to be typed in full — any unique fragment resolves (`tape show 7dd2afaf`), and `@last` refers to the most recent session. Agent names also accept a two-letter shorthand everywhere a `--agent` / `--to` flag appears:

| agent | short | | agent | short | | agent | short |
|---|---|---|---|---|---|---|---|
| claude-code | `cc` | | opencode | `oc` | | qwen | `qw` |
| codex | `cx` | | gemini | `gm` | | qoder | `qd` |
| cursor | `cu` | | antigravity | `ag` | | iflow | `if` |
| mimocode | `mi` | | kimi-code | `kc` | | aider | `ad` |

So `tape restore @last --to cc` and `tape export --agent cx --since 7d` do exactly what you'd hope. Case and separators don't matter (`Claude_Code`, `claude code` and `ClaudeCode` all resolve). Typos get a "did you mean" hint.

## Searching

```bash
tape search "race condition" --agent codex --since 30d --limit 10
tape search "会话备份"          # CJK queries just work
tape search "auth" --dir .     # only this project's sessions
```

Most session-search tools tokenize for English only and silently fail on CJK text. Tape tokenizes latin text by word (with prefix matching) and CJK text by overlapping character bigrams, indexed in SQLite FTS5. Each session also gets a synthetic `@meta` row containing its agent, title and project, so queries like `tape search codex` or `tape search "auth migration"` reach the session even when no message body uses those words. Search hits show the matching snippet, not just the session.

Default sort is **`recent`** — newest session first, capped at five hits per session so a chatty old conversation can't push fresh matches off the page. Pass `--sort relevance` for classic BM25 ranking (handy when mining old archives for a rare term).

`ls` and `search` both paginate: `--limit N --page P` (1-based). JSON output carries `page`, `page_size`, `has_more` (plus `total` for `ls`) so scripts and agents can stream through the archive without parsing the human footer.

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

## Exporting

```bash
tape export                                          # everything → tape-export-<ts>.tar.zst
tape export snapshot.tar.gz --compress gzip          # explicit name + codec
tape export --agent codex --since 7d                 # just codex, last week
tape export --exclude-agent cursor,opencode          # everything except those two
tape export --exclude-host local                     # only remote-mirrored sessions
tape export --dir . --format zip --compress none     # current project as a .zip
tape export --split-by agent                         # one file per agent
tape export --split-by month --since 1y              # monthly chunks for the year
tape export --split-by size --split-size 200M        # 200 MiB buckets, sessions never split
tape export --split-by agent --jobs 4                # 4 chunks at a time, threads divided
tape export --scan-only --agent claude-code          # audit secrets without writing
```

`tape export` walks the archive (optionally filtered with the same flags ls/search use) and writes one file — or a set of chunks. Pick the container with `--format tar|zip` and the codec with `--compress zstd|gzip|xz|none`; defaults are `tar` + `zstd`, the smallest and fastest combo. Output goes to `tape-export-<UTC-timestamp>.<ext>` in the current directory when you don't pass `-o`.

For very large archives use `--split-by` to fan out into multiple files instead of one giant one — `agent` (one file per agent), `month` (calendar months of session updated-at), or `size` (greedy buckets honoring `--split-size`, default 256 MiB; sessions are never split across chunks). The chunk suffix is inserted before the extension: `tape-export-<ts>.codex.tar.zst`, `tape-export-<ts>.part-001.tar.zst`, etc.

`--jobs N` controls parallelism: chunks run concurrently (up to `min(N, chunk-count)`) and the leftover thread budget feeds each chunk's zstd encoder, so total threads stay around `N` instead of `N²`. `--jobs 0` (the default) uses `GOMAXPROCS`; `--jobs 1` is a reproducible single-thread baseline. Even a single-file export benefits — zstd is already multi-threaded by default, this flag just lets you cap it.

Every filter flag has an `--exclude-*` counterpart: `--exclude-agent`, `--exclude-dir`, `--exclude-host`. Each is repeatable (or comma-separated) and accepts the same shorthands as the positive flag. The common idiom "everything except *that one agent*" is `tape export --exclude-agent cursor`; "remote machines only" is `tape ls --exclude-host local`. The reverse filters are layered AND-NOT, so you can combine positive + negative: `tape search "auth" --agent codex --exclude-dir /tmp` means "codex sessions matching auth, but not the throwaway ones".

What happens to the file is your choice — tape doesn't ship a "push to S3/git/Drive" command. Any tar / zip is trivially extractable with system tools and uploadable with whatever tool you already use; we'd rather get out of the way than reinvent rclone.

Real API keys end up in coding sessions more often than you think. By default `tape export` redacts secrets in stream (AWS, GitHub, OpenAI, Anthropic, Slack, JWT, private keys, …) with `[REDACTED:<rule>]` for text and length-preserving masks for binaries; local files are never modified. Pass `--no-redact` to opt out, or `--scan-only` to list what would be redacted without writing anything.

Sync is incremental too — a session is rehashed (and re-archived) only when its source files change size or mtime; the blake3 checksum stays the source of truth when stamps disagree. Sync also keeps the search index in sync with the archive: if you delete `~/.tape/index` or upgrade tape's tokenizer, the next `tape sync` quietly rebuilds it for you. No `index rebuild` command to remember.

## Keeping the archive fresh

`tape sync` is idempotent and cheap, but you still have to remember to run it. Pick one:

- **Manual** — run it before `ls` / `search` when freshness matters. Simplest, zero moving parts.
- **Shell hook** — drop `tape sync >/dev/null 2>&1 &` into your `.zshrc` / `.bashrc`; one fire-and-forget on every new shell.
- **Scheduled** — `tape sync --install [--interval 1h]` registers a user-scoped job that runs sync on a cadence:
  - **Linux:** a `systemd --user` timer at `~/.config/systemd/user/tape-sync.timer`
  - **macOS:** a LaunchAgent at `~/Library/LaunchAgents/com.tapeai.sync.plist`
  - **Windows:** prints the `schtasks /Create` one-liner for you to run (we don't auto-execute it).
  - `tape sync --status` reports whether one is installed; `tape sync --uninstall` removes it.

The job always runs as your user — never root. Add `--remote user@host` to `tape sync --install` to schedule a remote-aware sync; the flag is preserved in the unit file.

## Staying up to date

`tape version` reports the running build's version, commit, build date, Go toolchain, target platform, and the install method it detected (`npm`, `go-install`, `homebrew` or `manual`). The detector drives `tape update`:

```bash
tape version              # who am I, where do I live, how was I installed
tape update --check       # any newer release? (network call, exits non-zero if so)
tape update               # upgrade in place using the matching installer
tape update --channel beta --dry-run
```

tape never pings GitHub or Gitee in the background. Update checks happen only when you ask — `tape update --check` or `tape update`. Scripts can lean on the stable JSON envelope: `tape update --check --json | jq -e '.up_to_date'`.

### Release mirrors (GitHub + Gitee)

Every release lands on both [GitHub](https://github.com/chenhg5/tape) and [Gitee](https://gitee.com/cg33/tape). Tape picks the faster one for you at install + update time by sending a tiny HEAD probe to each and racing — typically the right answer in under 1.5s — so mainland-China users get the Gitee path automatically without configuring anything.

```bash
tape update -v               # see the per-mirror probe latency in the debug output
tape update --mirror gitee   # pin a specific mirror for one command
TAPE_MIRROR=gitee tape update  # or pin via env for the whole shell
```

The same probe is built into `scripts/install.sh` (curl-pipe-bash one-liner). See `docs/RELEASE.md` for the release-time double-push playbook (every binary + sha256 sidecar must land on both mirrors before users notice).

## Operations log + history

Every sync / export / restore / update writes one JSONL line to `~/.tape/operations.log` capturing the scope (flags), counts (archived / skipped / parts), bytes, duration and exit code. `tape history` is the human view; `tape history --json` is the machine view. Set `TAPE_NO_OPLOG=1` to silence the recorder if you'd rather not keep an audit trail.

## Persistent defaults

`tape config` is a tiny CRUD over `~/.tape/config.json`:

```bash
tape config set defaults.exclude_agents cursor,opencode    # always hide these
tape config set defaults.jobs 4                            # default --jobs for export
tape config set defaults.format zip                        # default container
tape config list                                           # see everything
tape config unset defaults.jobs                            # revert to CLI default
tape config path                                           # absolute path of the file
```

CLI flags still win when explicitly passed. For slice values (`exclude_agents`, `exclude_dirs`, `exclude_hosts`), the configured defaults are *merged* with what you pass on the command line — config is your baseline preference; flags are per-invocation additions.

## Shell completion

```bash
tape completion bash > /etc/bash_completion.d/tape          # system-wide
tape completion zsh  > "${fpath[1]}/_tape"                  # per-user zsh
tape completion fish > ~/.config/fish/completions/tape.fish # per-user fish
tape completion powershell | Out-String | Invoke-Expression # PowerShell
```

Re-run after a tape upgrade so new subcommands and flags get picked up.

## Uninstalling

```bash
tape uninstall --dry-run    # preview every removal step
tape uninstall              # interactive confirm, then go
tape uninstall --keep-archive   # only remove the scheduler + oplog
tape uninstall --force --json   # CI / scripted teardown
```

`tape uninstall` cleans up everything tape installed under your user account — the scheduler unit, the archive + index, the operations log — and prints the install-method-specific command to remove the binary itself. We deliberately don't `rm /usr/local/bin/tape` for you: bash users notice, agents might not, both deserve consent.

## Agent & script mode

Tape follows the [agent-cli-guide](https://github.com/Johnixr/agent-cli-guide) conventions throughout, so other AI agents (and your scripts) can drive it reliably:

```console
$ tape search "auth refactor" --limit 5 | jq .data.hits[0].session_id
"claude-code/7dd2afaf"

$ tape schema export        # what flags does this command take?
```

- **JSON by default when piped** — every command emits one `{"schema_version":1,"data":{...}}` object on stdout when it is not a TTY (or with `--json`). Colors honor `NO_COLOR`.
- **Semantic exit codes** — `0` ok · `1` error · `2` usage · `3` no results · `10` dry-run passed. Agents branch on codes, not on prose.
- **Machine-readable errors** — `{"error":"secrets_found","message":"...","suggestion":"...","retryable":false}` on stderr.
- **`--dry-run` everywhere** state is touched; exit 10 means "safe to run for real".
- **Drop-in agent skill** — [SKILL.md](SKILL.md) teaches any agent the workflows and contracts; copy it into your skills directory (e.g. `.cursor/skills/tape/` or `~/.claude/skills/tape/`).

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

Status legend: **active** = vendor-supported · **succeeded by …** = vendor announced a replacement · **EOL** = service shut down (tape still reads the on-disk history).

| Agent | Status | Reads | Native restore | Memory file |
|---|---|---|---|---|
| Claude Code | active | `~/.claude/projects/*/*.jsonl` | yes | `CLAUDE.md` |
| Codex CLI | active | `~/.codex/sessions/**/rollout-*.jsonl` | yes | `AGENTS.md` |
| Cursor CLI | active | `~/.cursor/chats/*/*/store.db` | memory / brief | `AGENTS.md` |
| OpenCode | active | `~/.local/share/opencode/opencode.db` | memory / brief | `AGENTS.md` |
| Gemini CLI | succeeded by **Antigravity CLI** 2026-06-18 (free/Pro/Ultra) | `~/.gemini/tmp/*/chats/session-*.jsonl` | memory / brief | `GEMINI.md` |
| Antigravity CLI | active (Gemini CLI successor) | `~/.gemini/antigravity-cli/brain/*/.system_generated/logs/transcript_full.jsonl` | memory / brief | `GEMINI.md` |
| Qwen Code | active | `~/.qwen/projects/*/chats/*.jsonl` | memory / brief | `QWEN.md` |
| iFlow CLI | **EOL** 2026-04-17, succeeded by Qoder | `~/.iflow/{projects,conversations}/...` | memory / brief | `IFLOW.md` |
| Qoder CLI | active (iFlow CLI successor) | `~/.qoder/projects/*/*.jsonl` | memory / brief | `AGENTS.md` |
| MiMo Code | active (Xiaomi, OpenCode fork) | `~/.local/share/mimocode/mimocode.db` | memory / brief | `MEMORY.md` |
| Kimi Code | active (Moonshot AI, supersedes kimi-cli) | `~/.kimi-code/sessions/*/*/agents/main/wire.jsonl` | memory / brief | `AGENTS.md` |
| Aider | active | `~/.aider.chat.history.md` (+ project-local) | memory / brief | `CONVENTIONS.md` |

Each agent is a small adapter behind one interface ([`ports.Source`](internal/core/ports/ports.go)); adding a new one does not touch the core. Contributions for Cline, RooCode, OpenHands and others are welcome.

## Development

```bash
make ci      # everything CI runs: fmt, vet, race tests, coverage gate, smoke, e2e
make smoke   # fast sanity subset (~1s)
make e2e     # full end-to-end suite against the real binary
```

The test pyramid: unit tests per package, fixture tests for every session format parser, and an end-to-end suite that builds the actual binary and drives it against fake `$HOME` data — exit codes, JSON contracts, secret gates and disaster-recovery drills included. CI enforces formatting, `go vet`, the race detector, a coverage floor and cross-compilation for five platforms on every push.

Architecture deep-dive (中文): [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)

## Roadmap

- [x] **Archive & search** — claude-code, codex, cursor, opencode, gemini + antigravity, qwen, iflow + qoder, mimocode, kimi-code, aider; CJK tokenization
- [x] **Export** — tar/zip × zstd/gzip/xz/none matrix, filter-scoped slices, secret redaction in stream
- [x] **Restore** — native claude-code ↔ codex, memory injection into the project's `<AGENT>.md` for everything else, plus transcript / brief fallbacks
- [ ] **Memory** — distill `MEMORY.md` from session history; MCP server so agents can search past sessions mid-task
- [ ] More sources (Cline, RooCode, OpenHands, Continue.dev)

## License

[MIT](LICENSE)
