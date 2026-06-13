# npm/ — publish templates, NOT a publishable package

This directory holds the **source templates** for the npm distribution.
None of these files is a complete `package.json`/launcher on its own.

```
npm/
├── main-package.json      # @tapeai/tape (launcher manifest, has __VERSION__)
├── platform-package.json  # @tapeai/tape-<plat> (per-platform sub-package)
└── tape.js                # launcher source (has __VERSION__, __PACKAGE__)
```

`__VERSION__`, `__PACKAGE__`, `__PLATFORM__`, `__OS__`, `__CPU__` are
placeholders rewritten at publish time.

## DO NOT run `npm publish` in here

`cd npm/ && npm publish` will fail with

```
npm error code ENOENT
npm error path .../npm/package.json
```

…because there is no `package.json` — only `*-package.json` templates.
That is the point: publishing requires building the per-platform Go
binaries first, then renderring the templates into `dist/npm/`.

## How to publish

Always go through the script — it builds binaries, renders templates,
and publishes 6 packages (1 main + 5 platform sub-packages) in the
correct order:

```bash
# from repo root, NOT from npm/
scripts/release-npm.sh 0.2.0              # stable
scripts/release-npm.sh 0.3.0-beta.1 beta  # beta channel
NPM_DRY_RUN=1 scripts/release-npm.sh 0.2.0  # safe rehearsal
```

The renderred artifacts land in `dist/npm/@tapeai/tape*/` and that is
where the actual `npm publish` calls happen. See
[`docs/RELEASE.md`](../docs/RELEASE.md) for the full release playbook.
