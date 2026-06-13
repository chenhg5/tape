#!/usr/bin/env bash
# Publish tape to npm as a single package.
#
# The package itself ships only ~10 KB (install.js + run.js +
# package.json + README.md). On `npm install` the postinstall hook
# fetches the right prebuilt binary for the user's OS/arch from the
# release mirror (GitHub or Gitee, fastest wins) and verifies sha256.
#
# This intentionally does NOT cross-compile binaries or publish
# per-platform sub-packages — we already host the same binaries on
# the GitHub + Gitee releases (built by release-binaries.sh), so
# republishing them inside npm packages is pure duplication and gave
# us six packages to keep in sync per release. See npm/README.md for
# the trade-off table.
#
# Usage:
#   scripts/release-npm.sh <version> [latest|beta]
#
#   scripts/release-npm.sh 0.2.0              # stable:  npm i -g @tapeai/tape
#   scripts/release-npm.sh 0.3.0-beta.1 beta  # beta:    npm i -g @tapeai/tape@beta
#
# Prerequisites:
#   - `scripts/release-binaries.sh <version>` has already published
#     the matching binaries to both GitHub and Gitee releases — the
#     npm postinstall fetches from there.
#   - `npm login` (with publish rights on the @tapeai org).
#
# Environment:
#   NPM_PACKAGE   package name (default: @tapeai/tape)
#   NPM_DRY_RUN   set to 1 to run `npm publish --dry-run` (nothing uploaded)
set -euo pipefail

# Refuse to run from anywhere except the repo root. The templates
# in npm/ are not a publishable package on their own — package.json
# carries __VERSION__ / __PACKAGE__ placeholders.
if [ ! -f npm/package.json ] || [ ! -d cmd/tape ]; then
  echo "error: run from the repo root (cwd: $(pwd))" >&2
  echo "       expected ./npm/package.json and ./cmd/tape/" >&2
  echo "       not from inside npm/ — see npm/README.md for the why." >&2
  exit 2
fi

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

rm -rf "$OUT"
mkdir -p "$OUT/$PKG"

echo "==> rendering $PKG@$VERSION (tag: $TAG)"
sed -e "s|__PACKAGE__|$PKG|g" -e "s|__VERSION__|$VERSION|g" \
  npm/package.json > "$OUT/$PKG/package.json"
cp npm/install.js "$OUT/$PKG/install.js"
cp npm/run.js     "$OUT/$PKG/run.js"
cp README.md      "$OUT/$PKG/README.md" 2>/dev/null || true
chmod +x "$OUT/$PKG/run.js" "$OUT/$PKG/install.js"

# Sanity: refuse to publish if the matching binary asset is missing
# from the GitHub release. The postinstall download would 404 for
# every user, so the npm package is useless. Gitee is checked too
# but not required — install.js races, and as long as one mirror has
# the asset, install works.
if [ "${NPM_DRY_RUN:-}" != "1" ]; then
  echo "==> verifying release assets are live"
  probe="tape-linux-amd64"
  gh_url="https://github.com/chenhg5/tape/releases/download/v${VERSION}/${probe}.sha256"
  if ! curl -fsI -o /dev/null --max-time 5 "$gh_url"; then
    echo "error: ${probe}.sha256 missing on GitHub v${VERSION}" >&2
    echo "       run scripts/release-binaries.sh ${VERSION} and upload first." >&2
    exit 3
  fi
  echo "    GitHub v${VERSION} assets OK"
fi

echo "==> publishing $PKG@$VERSION (tag: $TAG)"
npm publish "$OUT/$PKG" --tag "$TAG" --access public "${DRY_FLAG[@]}"

echo "done. install with:"
if [ "$TAG" = "beta" ]; then
  echo "  npm install -g $PKG@beta"
else
  echo "  npm install -g $PKG"
fi
echo "or pin a specific version:"
echo "  npm install -g $PKG@$VERSION"
echo
echo "CN-friendly:"
echo "  npm install -g $PKG --registry=https://registry.npmmirror.com"
echo "  TAPE_MIRROR=gitee npm install -g $PKG       # force the postinstall mirror"
