# npm/ — publish templates, NOT a publishable package

This directory is the **source** for the npm distribution. The
`package.json` here carries `__VERSION__` / `__PACKAGE__`
placeholders that get rewritten at publish time, so it isn't a real
manifest until `scripts/release-npm.sh` has rendered it.

```
npm/
├── package.json    # manifest with __VERSION__ / __PACKAGE__ placeholders
├── install.js      # postinstall: race GitHub vs Gitee, fetch raw binary, verify sha256
├── run.js          # bin: exec ./bin/tape, self-heal via install.js if missing/wrong
└── README.md       # you are here
```

## DO NOT run `npm publish` in here

`cd npm/ && npm publish` will succeed but publish a broken package
(version = literal `__VERSION__`). The release script renders the
placeholders into `dist/npm/@tapeai/tape/` and publishes from there:

```bash
# from repo root, NOT from npm/
scripts/release-npm.sh 0.2.0              # stable
scripts/release-npm.sh 0.3.0-beta.1 beta  # beta channel
NPM_DRY_RUN=1 scripts/release-npm.sh 0.2.0  # safe rehearsal, no upload
```

## How install works (for the curious)

We deliberately ship **one package**, not one main + five
platform-specific sub-packages. The trade-off:

| approach | pros | cons |
|---|---|---|
| 5 sub-packages via `optionalDependencies` | binary bundled with package, zero post-install network | npm/yarn/pnpm/CI all skip optionals in different cases; CN mirrors lag the sub-packages; six packages to publish per release |
| single package + `postinstall` download | one package to publish; download = a binary we already host on the release; mirror-aware so CN users transparently get Gitee | requires network at install time (falls back to lazy fetch on first run if blocked) |

The download path:

1. `postinstall` runs `install.js`
2. Race a 2.5s HEAD probe against GitHub and Gitee — fastest wins
3. Download the raw binary (`tape-<os>-<arch>[.exe]`) + its `.sha256`
4. Verify, `chmod +x`, clear macOS quarantine, drop into `bin/`
5. If anything fails, `install.js` warns and **exits 0** — never breaks `npm install`
6. `run.js` self-heals: missing/wrong binary triggers a re-run of `install.js`

Pin a mirror with `TAPE_MIRROR=github` or `TAPE_MIRROR=gitee` —
respected by `install.js`, `run.js`, the embedded `tape update`
command, and `scripts/install.sh`. One env var for the whole story.

See [`docs/RELEASE.md`](../docs/RELEASE.md) for the full release
playbook (GitHub + Gitee dual push, binary release, npm publish).
