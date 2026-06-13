#!/usr/bin/env bash
# Build platform binaries and publish tape to npm.
#
# Usage:
#   scripts/release-npm.sh <version> [latest|beta]
#
#   scripts/release-npm.sh 0.2.0              # stable:  npm i -g @tapeai/tape
#   scripts/release-npm.sh 0.3.0-beta.1 beta  # beta:    npm i -g @tapeai/tape@beta
#
# Environment:
#   NPM_PACKAGE   package name (default: @tapeai/tape). Platform packages
#                 become <name>-linux-x64 etc. Scoped names like the
#                 default produce @tapeai/tape-linux-x64; unscoped names
#                 produce e.g. tape-cli-linux-x64.
#   NPM_DRY_RUN   set to 1 to run `npm publish --dry-run` (nothing uploaded)
#
# Requires: go, npm (logged in: `npm login`, with publish rights on the
# @tapeai org for the default name), run from the repo root. Scoped
# packages always need --access public on first publish; the script
# passes it unconditionally so re-publishing is safe.
set -euo pipefail

VERSION=${1:?usage: release-npm.sh <version> [latest|beta]}
TAG=${2:-latest}
PKG=${NPM_PACKAGE:-@tapeai/tape}
OUT=dist/npm
DRY_FLAG=()
[ "${NPM_DRY_RUN:-}" = "1" ] && DRY_FLAG=(--dry-run)

case "$TAG" in latest|beta) ;; *) echo "tag must be latest or beta" >&2; exit 2 ;; esac
if [ "$TAG" = "beta" ] && [[ "$VERSION" != *-* ]]; then
  echo "warning: beta tag with a non-prerelease version ($VERSION); consider X.Y.Z-beta.N" >&2
fi

# platform key -> GOOS GOARCH npm-os npm-cpu
PLATFORMS=(
  "linux-x64    linux   amd64 linux  x64"
  "linux-arm64  linux   arm64 linux  arm64"
  "darwin-x64   darwin  amd64 darwin x64"
  "darwin-arm64 darwin  arm64 darwin arm64"
  "win32-x64    windows amd64 win32  x64"
)

rm -rf "$OUT"
mkdir -p "$OUT"

echo "==> building $PKG@$VERSION (tag: $TAG)"
for entry in "${PLATFORMS[@]}"; do
  read -r key goos goarch npmos npmcpu <<<"$entry"
  pkgdir="$OUT/$PKG-$key"
  bin="tape"; [ "$goos" = "windows" ] && bin="tape.exe"

  mkdir -p "$pkgdir/bin"
  CGO_ENABLED=0 GOOS=$goos GOARCH=$goarch \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
    -o "$pkgdir/bin/$bin" ./cmd/tape

  sed -e "s|__PACKAGE__|$PKG|g" \
      -e "s|__VERSION__|$VERSION|g" \
      -e "s|__PLATFORM__|$key|g" \
      -e "s|__OS__|$npmos|g" \
      -e "s|__CPU__|$npmcpu|g" \
      npm/platform-package.json > "$pkgdir/package.json"
  echo "    built $PKG-$key"
done

# main package: launcher + manifest
maindir="$OUT/$PKG"
mkdir -p "$maindir"
sed -e "s|__PACKAGE__|$PKG|g" -e "s|__VERSION__|$VERSION|g" \
  npm/main-package.json > "$maindir/package.json"
sed -e "s|^const PACKAGE_NAME = .*|const PACKAGE_NAME = \"$PKG\";|" \
  npm/tape.js > "$maindir/tape.js"
chmod +x "$maindir/tape.js"
cp README.md "$maindir/README.md" 2>/dev/null || true

echo "==> publishing platform packages"
for entry in "${PLATFORMS[@]}"; do
  read -r key _ <<<"$entry"
  npm publish "$OUT/$PKG-$key" --tag "$TAG" --access public "${DRY_FLAG[@]}"
done

echo "==> publishing $PKG"
npm publish "$maindir" --tag "$TAG" --access public "${DRY_FLAG[@]}"

echo "done. install with:"
if [ "$TAG" = "beta" ]; then
  echo "  npm install -g $PKG@beta"
else
  echo "  npm install -g $PKG"
fi
echo "or pin a specific version:"
echo "  npm install -g $PKG@$VERSION"
