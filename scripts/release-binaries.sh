#!/usr/bin/env bash
# release-binaries.sh: build cross-platform binaries and package
# them as the tarballs install.sh / `tape update` (manual install
# method) expect to find on GitHub + Gitee Releases.
#
# Usage:
#   scripts/release-binaries.sh <version>
#
#   scripts/release-binaries.sh 0.1.0
#   scripts/release-binaries.sh 0.2.0-beta.1
#
# Output: dist/release/
#   # raw, ready-to-exec binaries (install.sh default; Docker COPY-friendly;
#   # Windows doesn't need unzip):
#   tape-linux-amd64          tape-linux-amd64.sha256
#   tape-linux-arm64          tape-linux-arm64.sha256
#   tape-darwin-amd64         tape-darwin-amd64.sha256
#   tape-darwin-arm64         tape-darwin-arm64.sha256
#   tape-windows-amd64.exe    tape-windows-amd64.exe.sha256
#
#   # bundled tarballs (binary + LICENSE + README; for brew tap, manual
#   # download, anyone who prefers a single archive):
#   tape_<ver>_linux_amd64.tar.gz          tape_<ver>_linux_amd64.tar.gz.sha256
#   tape_<ver>_linux_arm64.tar.gz          tape_<ver>_linux_arm64.tar.gz.sha256
#   tape_<ver>_darwin_amd64.tar.gz         tape_<ver>_darwin_amd64.tar.gz.sha256
#   tape_<ver>_darwin_arm64.tar.gz         tape_<ver>_darwin_arm64.tar.gz.sha256
#   tape_<ver>_windows_amd64.zip           tape_<ver>_windows_amd64.zip.sha256
#
#   SHA256SUMS                # one-line-per-file aggregate over everything
#
# install.sh prefers the raw binary (one curl, no tar/unzip dep) and
# falls back to the tarball when the raw asset isn't on the release
# (e.g. older v0.x.y published before this script grew the dual
# output). Both naming schemes are part of the public contract —
# don't rename without updating install.sh in lockstep or every CN
# user gets a 404.
#
# Why this is a separate script from release-npm.sh:
#   * npm publishes 6 packages (1 main + 5 platform), no zipping
#     needed — npm hosts the binaries itself inside platform-pkg
#     tarballs. The packaging format here (tar.gz with binary +
#     LICENSE + README + .sha256) is for users *outside* the npm
#     install path (curl|bash, homebrew, manual).
#   * Splitting the scripts lets the npm side stay self-contained
#     and lets CI publish them in parallel (no shared output
#     directory race).
#
# Requires: go, sha256sum (or shasum -a 256), tar, zip.
set -euo pipefail

VERSION=${1:?usage: release-binaries.sh <version>}
OUT=dist/release
PROJECT="github.com/chenhg5/tape"

c_reset='\033[0m'; c_green='\033[32m'; c_gray='\033[90m'
if [ ! -t 1 ]; then c_reset=''; c_green=''; c_gray=''; fi
info() { printf "  %b\n" "${c_gray}·${c_reset} $1"; }
ok()   { printf "  %b\n" "${c_green}✓${c_reset} $1"; }

# Cross-platform sha helper: macOS ships shasum, Linux ships
# sha256sum, both produce the same "<sum>  <name>" output we want.
sha256() {
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum "$1"
    else
        shasum -a 256 "$1"
    fi
}

# tape_<ver>_<os>_<arch> layout for both the staging dir AND the
# archive members. Users who tar -xzf get a single top-level dir
# they can `mv ... /usr/local/bin/tape` without guessing.
PLATFORMS=(
    "linux   amd64 tar.gz"
    "linux   arm64 tar.gz"
    "darwin  amd64 tar.gz"
    "darwin  arm64 tar.gz"
    "windows amd64 zip"
)

rm -rf "$OUT"
mkdir -p "$OUT"

echo "==> building tape v$VERSION for 5 platforms"
for entry in "${PLATFORMS[@]}"; do
    read -r goos goarch ext <<<"$entry"
    name="tape_${VERSION}_${goos}_${goarch}"
    archive="${name}.${ext}"
    stage="$OUT/_stage/$name"
    bin="tape"; [ "$goos" = "windows" ] && bin="tape.exe"

    # Raw binary asset: cosign/sops/age-style "curl + chmod +x +
    # mv" target. Naming follows the GoReleaser default that most
    # users have muscle memory for: <name>-<os>-<arch>[.exe].
    raw="tape-${goos}-${goarch}"
    [ "$goos" = "windows" ] && raw="${raw}.exe"

    mkdir -p "$stage"
    # -trimpath strips local FS paths from the binary (reproducibility).
    # -s -w strips symbol + DWARF tables (~30% size reduction; we
    # don't ship debug symbols, users debug via --debug + logs).
    # -X main.version pins the runtime-reported version so
    # `tape version` matches the tag.
    CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
        go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
        -o "$stage/$bin" ./cmd/tape

    # 1) Drop the raw binary at the OUT root with its publish-time
    # name. Single file, no archive — install.sh prefers this
    # because curl -> chmod -> mv is one less subprocess (no tar
    # / unzip) and Windows users don't need an extracting tool.
    cp "$stage/$bin" "$OUT/$raw"
    chmod +x "$OUT/$raw"
    (cd "$OUT" && sha256 "$raw" > "${raw}.sha256")
    ok "$raw ($(du -h "$OUT/$raw" | cut -f1))"

    # 2) Bundle LICENSE + README + binary into the user-facing
    # archive. For tar.gz/zip we cd to _stage so the archive's
    # top-level entry is `<name>/` — both Windows Explorer and
    # `unzip` do the right thing then.
    cp LICENSE "$stage/" 2>/dev/null || true
    cp README.md "$stage/" 2>/dev/null || true
    (
        cd "$OUT/_stage"
        case "$ext" in
            tar.gz) tar -czf "../${archive}" "$name" ;;
            zip)    zip -q -r "../${archive}" "$name" ;;
        esac
    )
    (cd "$OUT" && sha256 "$archive" > "${archive}.sha256")
    ok "$archive ($(du -h "$OUT/$archive" | cut -f1))"
done
rm -rf "$OUT/_stage"

# Aggregate file: convenience for someone who wants to verify
# every asset with one `sha256sum -c SHA256SUMS`. It's *also* a
# useful diff target between mirrors — `diff` against Gitee's
# copy proves both hosts serve identical bits.
(
    cd "$OUT"
    cat *.sha256 > SHA256SUMS
)
ok "SHA256SUMS"

echo
echo "==> $OUT (ready to upload to GitHub + Gitee releases)"
ls -lh "$OUT" | awk 'NR>1 {printf "    %-50s %s\n", $9, $5}'
echo
echo "Next steps (notes are sourced from CHANGELOG.md; no parallel file tree):"
echo "  1. GitHub release with English notes pulled from CHANGELOG.md:"
echo "     gh release create v$VERSION $OUT/* \\"
echo "         --title \"tape v$VERSION\" \\"
echo "         --notes \"\$(awk '/^## \\[/{if(p)exit; if(\$0~/$VERSION/){p=1;next}} p' CHANGELOG.md)\""
echo "  2. Gitee release (web UI): https://gitee.com/cg33/tape/releases/new"
echo "     paste the Chinese template from docs/RELEASE.md and attach $OUT/*"
echo "  3. npm publish (6 packages): scripts/release-npm.sh $VERSION"
