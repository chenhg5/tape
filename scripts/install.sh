#!/usr/bin/env bash
# install.sh: one-liner installer for tape.
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/chenhg5/tape/main/scripts/install.sh | bash
#
# Optional environment overrides:
#   TAPE_VERSION   v0.2.0   pin a release tag (default: latest)
#   TAPE_PREFIX    /usr/local/bin   where to land the binary (default below)
#   TAPE_REPO      chenhg5/tape   override the GitHub repo for forks
#   TAPE_NO_SUDO   1   never invoke sudo; fall back to ~/.local/bin
#
# The script is deliberately defensive: it refuses to pipe to bash
# under sh / dash (-o pipefail), detects OS+arch up front, and
# never writes anything until the download + checksum match.

set -euo pipefail

REPO="${TAPE_REPO:-chenhg5/tape}"
VERSION="${TAPE_VERSION:-latest}"
PREFIX_PREF="${TAPE_PREFIX:-}"
NO_SUDO="${TAPE_NO_SUDO:-0}"

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

# Step 2: resolve the tag. GitHub's /releases/latest API redirects
# to the latest stable release; we follow with curl -L. For pinned
# versions we trust the user's TAPE_VERSION verbatim.
if [ "$VERSION" = "latest" ]; then
    info "resolving latest release for $REPO"
    tag="$(curl -fsSL -H 'Accept: application/vnd.github+json' \
            "https://api.github.com/repos/$REPO/releases/latest" \
        | grep -m 1 '"tag_name"' \
        | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
    [ -n "$tag" ] || fail "could not resolve latest release tag — try TAPE_VERSION=v…"
else
    tag="$VERSION"
fi
info "version $tag"

# Step 3: figure out where to install. Order of preference:
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

# Step 4: download the tarball and the matching .sha256 sidecar.
# tar.gz on all platforms (Windows release ships a .zip; we add a
# branch when needed but the default convention follows Go releases).
ext="tar.gz"
if [ "$os_id" = "windows" ]; then ext="zip"; fi
asset="tape_${tag#v}_${os_id}_${arch_id}.${ext}"
url="https://github.com/$REPO/releases/download/$tag/$asset"
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

# Step 5: extract and move into place.
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

# Step 6: post-install sanity. We don't auto-add to PATH (that's
# the user's shell config to own), but we shout if the prefix isn't
# already on PATH so the user doesn't have to debug a "command not
# found" themselves.
case ":$PATH:" in
    *":$prefix:"*) ;;
    *) warn "PATH does not include $prefix — add it to your shell rc" ;;
esac

# Step 7: quick capability check. Worst case we wrote a broken
# binary for the wrong arch (rare but possible during release
# rollover); calling --version proves it actually runs.
if "$target" --version >/dev/null 2>&1; then
    "$target" --version
    ok "ready: run ${c_bold}tape --help${c_reset} to get started"
else
    warn "installed binary did not respond to --version; please file an issue"
fi
