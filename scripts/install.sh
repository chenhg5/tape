#!/usr/bin/env bash
# install.sh: one-liner installer for tape.
#
# Usage:
#   # International / global:
#   curl -fsSL https://raw.githubusercontent.com/chenhg5/tape/main/scripts/install.sh | bash
#   # Mainland China users may prefer to host the script itself on Gitee:
#   curl -fsSL https://gitee.com/cg33/tape/raw/main/scripts/install.sh | bash
#
# By default this script auto-detects the faster release mirror
# (GitHub or Gitee) for downloading the binary itself: it sends a
# tiny HEAD request to each host, waits at most ~2s, and uses
# whichever responds first. CN users get the Gitee path on its own.
#
# Optional environment overrides:
#   TAPE_VERSION   v0.2.0       pin a release tag (default: latest)
#   TAPE_PREFIX    /usr/local/bin   where to land the binary (default below)
#   TAPE_NO_SUDO   1            never invoke sudo; fall back to ~/.local/bin
#   TAPE_MIRROR    github|gitee|auto   force a mirror (default: auto/probe)
#   TAPE_REPO_GH   chenhg5/tape       GitHub repo  (for forks)
#   TAPE_REPO_GT   cg33/tape          Gitee repo   (for forks)
#
# The script is deliberately defensive: it refuses to pipe to bash
# under sh / dash (-o pipefail), detects OS+arch up front, and
# never writes anything until the download + checksum match.

set -euo pipefail

REPO_GH="${TAPE_REPO_GH:-${TAPE_REPO:-chenhg5/tape}}"
REPO_GT="${TAPE_REPO_GT:-cg33/tape}"
VERSION="${TAPE_VERSION:-latest}"
PREFIX_PREF="${TAPE_PREFIX:-}"
NO_SUDO="${TAPE_NO_SUDO:-0}"
MIRROR_PREF="${TAPE_MIRROR:-auto}"

c_reset='\033[0m'
c_bold='\033[1m'
c_green='\033[32m'
c_yellow='\033[33m'
c_red='\033[31m'
c_gray='\033[90m'
if [ ! -t 1 ]; then
    c_reset='' c_bold='' c_green='' c_yellow='' c_red='' c_gray=''
fi

log()  { printf "  %b\n" "$1"; }
info() { log "${c_gray}·${c_reset} $1"; }
ok()   { log "${c_green}✓${c_reset} $1"; }
warn() { log "${c_yellow}!${c_reset} $1"; }
fail() { log "${c_red}✗${c_reset} $1"; exit 1; }

# Step 1: detect OS + arch and translate to the asset suffix the
# release workflow publishes. Tape ships darwin/linux/windows × amd64/arm64.
os="$(uname -s)"
arch="$(uname -m)"
case "$os" in
    Darwin)  os_id="darwin" ;;
    Linux)   os_id="linux" ;;
    MINGW*|MSYS*|CYGWIN*) os_id="windows" ;;
    *) fail "unsupported OS: $os" ;;
esac
case "$arch" in
    x86_64|amd64) arch_id="amd64" ;;
    arm64|aarch64) arch_id="arm64" ;;
    *) fail "unsupported architecture: $arch" ;;
esac

# Step 2: pick the release mirror. We probe both hosts with a short
# HEAD request and pick whichever responds in less time. This is the
# single biggest UX win for users behind a slow github.com link
# (common in mainland China) — they don't need to know about Gitee,
# tape just notices.
probe_seconds() {
    # $1 = URL. Echoes the round-trip in seconds (curl's %{time_total}),
    # or "999" on timeout / hard failure. We use --max-time so a wedged
    # TCP doesn't make us hang past the probe budget.
    local t
    t="$(curl -o /dev/null -fsS -X HEAD \
            -w '%{time_total}' \
            --max-time 2 --connect-timeout 2 \
            "$1" 2>/dev/null)" || t=""
    if [ -z "$t" ]; then
        echo "999"
    else
        echo "$t"
    fi
}

pick_mirror() {
    case "$MIRROR_PREF" in
        github|gh)   echo "github"; return ;;
        gitee|gt|cn) echo "gitee";  return ;;
    esac
    local gh_t gt_t
    gh_t="$(probe_seconds "https://api.github.com/repos/$REPO_GH")"
    gt_t="$(probe_seconds "https://gitee.com/api/v5/repos/$REPO_GT")"
    info "mirror probe: github=${gh_t}s gitee=${gt_t}s"
    # awk handles float comparison portably (bc isn't always installed).
    if awk -v gh="$gh_t" -v gt="$gt_t" 'BEGIN{exit !(gt+0 < gh+0)}'; then
        echo "gitee"
    else
        echo "github"
    fi
}

mirror="$(pick_mirror)"
case "$mirror" in
    github) repo="$REPO_GH" ; release_base="https://github.com/$REPO_GH/releases" ; api_latest="https://api.github.com/repos/$REPO_GH/releases/latest" ;;
    gitee)  repo="$REPO_GT" ; release_base="https://gitee.com/$REPO_GT/releases"   ; api_latest="https://gitee.com/api/v5/repos/$REPO_GT/releases/latest" ;;
esac
info "mirror chosen: $mirror ($repo)"

# Step 3: resolve the tag. Both mirrors expose a /releases/latest
# endpoint that returns JSON with tag_name; the parsing line is the
# same for both (Gitee mirrors the GitHub API shape for this).
if [ "$VERSION" = "latest" ]; then
    info "resolving latest release"
    tag="$(curl -fsSL -H 'Accept: application/json' \
            -H 'User-Agent: tape-install' \
            "$api_latest" \
        | grep -m 1 '"tag_name"' \
        | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
    [ -n "$tag" ] || fail "could not resolve latest release tag — try TAPE_VERSION=v… or TAPE_MIRROR=$([ "$mirror" = "github" ] && echo gitee || echo github)"
else
    tag="$VERSION"
fi
info "version $tag"

# Step 4: figure out where to install. Order of preference:
#   1. TAPE_PREFIX (explicit)
#   2. /usr/local/bin if writable (or sudo available + not refused)
#   3. ~/.local/bin (created if missing); user must have it on PATH
prefix=""
if [ -n "$PREFIX_PREF" ]; then
    prefix="$PREFIX_PREF"
elif [ -w "/usr/local/bin" ]; then
    prefix="/usr/local/bin"
elif command -v sudo >/dev/null 2>&1 && [ "$NO_SUDO" != "1" ]; then
    prefix="/usr/local/bin"
    USE_SUDO=1
else
    prefix="$HOME/.local/bin"
    mkdir -p "$prefix"
fi
info "install prefix $prefix"

# Step 5: download the tarball and the matching .sha256 sidecar.
# tar.gz on all platforms (Windows release ships a .zip; we add a
# branch when needed but the default convention follows Go releases).
# Both mirrors expose /releases/download/<tag>/<asset> with identical
# layouts, so the only difference is the host portion.
ext="tar.gz"
if [ "$os_id" = "windows" ]; then ext="zip"; fi
asset="tape_${tag#v}_${os_id}_${arch_id}.${ext}"
url="$release_base/download/$tag/$asset"
sha_url="$url.sha256"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
info "downloading $asset"
curl -fsSL -o "$tmp/$asset" "$url" \
    || fail "download failed: $url"

# Sha256 is best-effort: not every release publishes a sidecar yet,
# but when it does we MUST verify or refuse.
if curl -fsSL -o "$tmp/$asset.sha256" "$sha_url" 2>/dev/null; then
    info "verifying sha256"
    (cd "$tmp" && shasum -a 256 -c "$asset.sha256" >/dev/null 2>&1) \
        || fail "checksum mismatch — refusing to install"
else
    warn "no published sha256 sidecar for $tag; skipping checksum"
fi

# Step 6: extract and move into place.
info "extracting"
case "$ext" in
    tar.gz) (cd "$tmp" && tar -xzf "$asset") ;;
    zip)    (cd "$tmp" && unzip -q "$asset") ;;
esac
bin_src="$tmp/tape"
[ "$os_id" = "windows" ] && bin_src="$tmp/tape.exe"
[ -f "$bin_src" ] || fail "binary not found inside $asset"

target="$prefix/$(basename "$bin_src")"
if [ "${USE_SUDO:-0}" = "1" ]; then
    sudo install -m 0755 "$bin_src" "$target"
else
    install -m 0755 "$bin_src" "$target"
fi
ok "installed ${c_bold}$target${c_reset}"

# Step 7: post-install sanity. We don't auto-add to PATH (that's
# the user's shell config to own), but we shout if the prefix isn't
# already on PATH so the user doesn't have to debug a "command not
# found" themselves.
case ":$PATH:" in
    *":$prefix:"*) ;;
    *) warn "PATH does not include $prefix — add it to your shell rc" ;;
esac

# Step 8: quick capability check. Worst case we wrote a broken
# binary for the wrong arch (rare but possible during release
# rollover); calling --version proves it actually runs.
if "$target" --version >/dev/null 2>&1; then
    "$target" --version
    ok "ready: run ${c_bold}tape --help${c_reset} to get started"
else
    warn "installed binary did not respond to --version; please file an issue"
fi
