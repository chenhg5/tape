# Releasing tape

Tape ships from two upstreams, in parallel:

| Mirror | URL | Audience |
|--------|-----|----------|
| GitHub | `git@github.com:chenhg5/tape.git` | International / global; canonical npm + go-install origin |
| Gitee  | `git@gitee.com:cg33/tape.git`     | Mainland China; faster install + update for users behind throttled `github.com` |

`scripts/install.sh` and the `tape update` CLI both auto-select the
faster mirror at runtime (HEAD-probe race, ~1.5s budget). For that
to work, **every release must land on both hosts within a short
window** — otherwise CN users either get a stale `tape update` or
a 404 on the asset path.

This doc is the playbook for "I cut a tag, what do I have to do?".

## TL;DR

```bash
# 1) cut the tag locally
git tag -s v0.2.0 -m "tape v0.2.0"

# 2) push the tag to BOTH remotes
git push origin   v0.2.0    # github (canonical)
git push gitee    v0.2.0    # gitee mirror

# 3) GitHub Action builds the binaries, draft → publish release on github
# 4) Run the mirror-release script: download GH assets, re-upload to Gitee
./scripts/release-gitee.sh v0.2.0
```

If step 3 is wired through `.github/workflows/release.yml` (it
publishes a stable Release with the asset matrix + sha256 sidecars),
step 4 just copies what's already there over to Gitee.

## One-time setup

```bash
# Add the Gitee remote alongside origin (GitHub).
git remote add gitee git@gitee.com:cg33/tape.git
git remote -v        # confirm both
```

Pushing `--all` and `--tags` together is convenient but **noisy
on Gitee** (it tries to mirror every WIP branch). Push deliberately
instead:

```bash
git push origin main
git push gitee  main
```

A `git config alias.pushall '!git push origin && git push gitee'`
in `~/.gitconfig` makes "push to both" a one-liner without becoming
a global default.

## What needs to be on both hosts

| Asset | Where it must exist |
|-------|---------------------|
| Source code at the tagged commit | both |
| Release notes / changelog | both (Gitee renders the same Markdown) |
| Per-platform binaries (`tape_<ver>_<os>_<arch>.tar.gz`) | both |
| Matching `.sha256` sidecars | both — install.sh enforces them |

The asset *names* must match byte-for-byte. `install.sh` derives
the URL from the running OS+arch and the resolved tag; if Gitee's
asset is called `tape-0.2.0-linux-amd64.tar.gz` instead of
`tape_0.2.0_linux_amd64.tar.gz`, the install fails for CN users.
Keep the convention.

## npm + Go modules

These two distribution channels are **single-source** (GitHub only):

- **npm** (`@tapeai/tape`) pulls from `https://registry.npmjs.org`,
  which fetches from GitHub. We don't republish to a Gitee mirror
  registry — npm clients in CN typically use registry mirrors
  (e.g. `npmmirror.com`) that already cache the package.
- **Go modules** (`github.com/chenhg5/tape/cmd/tape`) pulls from
  the Go module proxy (`proxy.golang.org`). Mainland users typically
  set `GOPROXY=https://goproxy.cn,direct` which mirrors the same
  upstream.

So `tape update` for npm / go-install users doesn't go through the
mirror probe — the install method's own proxy story handles it.
The mirror probe matters specifically for **install.sh users**
(downloading a binary tarball) and for the **release metadata
fetch** (`tape update --check` hitting the API for a tag list).

## Verifying after release

```bash
# Did the binary land on both mirrors with matching sha256?
diff <(curl -fsSL https://github.com/chenhg5/tape/releases/download/v0.2.0/tape_0.2.0_linux_amd64.tar.gz.sha256) \
     <(curl -fsSL https://gitee.com/cg33/tape/releases/download/v0.2.0/tape_0.2.0_linux_amd64.tar.gz.sha256)

# Does `tape update --check` see the new tag from each mirror?
TAPE_MIRROR=github tape update --check --json | jq .latest
TAPE_MIRROR=gitee  tape update --check --json | jq .latest
```

Both should report the same `latest`. If Gitee lags, it's because
asset upload to Gitee is still in progress — wait a minute and
re-check, don't bypass.

## Hotfix policy

If a release is broken (bad binary, security CVE), **unpublish on
both** within the same change window:

```bash
gh release delete v0.2.0 --yes        # github
# Gitee: web UI → Releases → v0.2.0 → Delete
```

A half-deleted release where Gitee still serves the broken artifact
is worse than no release at all.
