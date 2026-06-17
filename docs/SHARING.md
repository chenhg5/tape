# Sharing & importing sessions

`tape share` (since v0.3.0) packages one or more agent sessions into a
single bundle file you can hand to a teammate, and `tape import` is
the symmetric receive-side that re-materializes those sessions inside
the other machine's `~/.tape` archive — atomically, with conflict
resolution, and with optional cwd rewriting for cross-machine paths.

The two commands deliberately stay small and composable: under the
hood they reuse the same `internal/export/snapshot` writer and
`internal/archive/local` reader that drive `tape export` and `tape
sync`. What's new in v0.3.0 is the **manifest** (`tape-bundle.json`
stamped at the bundle root), which carries the metadata the importer
needs to make routing decisions without unpacking the entire archive.

## Quick start

On machine A — share one session:

```bash
tape ls --limit 5                            # find the session id
tape share claude-code/abc123 -o ./debug.tar.zst
```

On machine B — import it:

```bash
tape import ./debug.tar.zst
tape ls                                      # the new row is there
tape restore claude-code/abc123 --to codex   # natively recreate it
```

That's the entire happy path. The rest of this doc is for when reality
disagrees with the happy path.

## Bundle layout

A v0.3.0 bundle is a tar (or zip) tree of:

```
tape-bundle.json                  # manifest, root entry
<agent>/<project_slug>/<sid>/
    session.json                  # canonical model.Session
    meta.json                     # archive metadata + checksum
    raw/                          # untouched agent-native files
```

`tape-bundle.json` lives at the root and contains:

```jsonc
{
  "schema_version": 1,
  "tape_version": "0.3.0",
  "kind": "share",
  "created_at": "2026-06-17T...",
  "source": {"host": "alice-mac", "tape_home": "/Users/alice/.tape"},
  "sessions": [
    {"id":"claude-code/abc123","agent":"claude-code","source_id":"abc123",
     "project_slug":"-Users-alice-code-proj","original_cwd":"/Users/alice/code/proj",
     "title":"refactor authn middleware","msg_count":47,
     "started_at":"...","updated_at":"...","checksum":"sha256:..."}
  ]
}
```

Bundles from tape **<= 0.2.0** do not carry this manifest. `tape
import` falls back to scanning the `<agent>/<project>/<sid>/` layout
directly, so old backups still work.

## Conflict resolution

When the importing archive already has a session with the same
`(agent, source_id)` pair, `tape import` chooses one of four
behaviours via `--on-conflict`:

| flag | behaviour | default if |
|---|---|---|
| `skip` | leave the existing copy untouched | non-TTY |
| `overwrite` | replace the existing copy's files | — |
| `rename` | stage the import under a fresh source id (`<old>-tape-<ts>`) so both copies coexist | — |
| `prompt` | ask interactively (`y` = overwrite, anything else = abort) | TTY |

Two things to know:

1. **Identical checksums always silent-skip.** If the on-disk
   `meta.json.checksum` matches the manifest's, the importer treats
   it as an idempotent re-run and skips with no message regardless of
   `--on-conflict`. This is what you want when you're re-importing the
   same bundle twice.
2. **Exit code 4 means "left some unresolved".** On non-TTY runs with
   the default `skip`, if any session was skipped *because* of a
   conflict (not because checksums matched), the command exits **4**
   so CI scripts can branch on "needs human attention" without losing
   the "ran successfully otherwise" signal.

## Cross-machine paths

A session captured on `/Users/alice/code/proj` is useless on a machine
that has the same project at `/root/code/proj`. Two knobs help:

- `--rewrite-cwd <new>` on `tape import`: rewrites every imported
  session's recorded cwd to `<new>` and recomputes the project_slug
  so the imported files land where this machine's agent will look for
  them.
- `tape restore` auto-rewrites: if the recorded cwd doesn't exist on
  this box, restore points the session at the current `pwd` before
  calling the native writer. This is per-restore, not per-import — the
  archive copy keeps the original cwd as historical record.

## Atomicity

`tape import` stages every session under
`~/.tape/.import-staging/<uuid>/` first and only moves them into the
live archive once **all** of them are ready. Any failure during stage
or commit removes the staging tree and leaves the existing archive
untouched. The FTS index is updated *after* the rename so a half-
committed index can't claim sessions whose files don't exist yet.

## JSON output

`tape import --json` (or any non-TTY run) emits one JSON object per
session on stdout:

```jsonl
{"id":"claude-code/abc123","action":"imported"}
{"id":"claude-code/def456","action":"renamed","new_id":"claude-code/def456-tape-20260617123045"}
{"id":"claude-code/ghi789","action":"skipped","reason":"exists; --on-conflict=skip"}
```

`action` is one of `imported`, `overwritten`, `renamed`, or `skipped`.
`reason` is present on skips. `new_id` is present on renames.

## See also

- [ARCHITECTURE.md](ARCHITECTURE.md) — bundle / Sink chapter for the
  internal data flow.
- `tape import --help`, `tape share --help`, `tape export --help` —
  flag reference.
