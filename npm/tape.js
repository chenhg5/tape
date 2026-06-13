#!/usr/bin/env node
// Thin launcher: resolve the prebuilt tape binary and exec it.
// Resolution order (first hit wins):
//   1. optionalDependency platform package (the common path —
//      `npm i -g @tapeai/tape` already pulled it as a sub-package)
//   2. ~/.tape/bin/tape-<version>[.exe] — a previously self-fetched
//      binary cached from a prior `tape ...` invocation
//   3. Lazy download from the release mirrors:
//        GitHub (chenhg5/tape) raced against Gitee (cg33/tape);
//        the first one to answer HEAD wins, the loser is cancelled.
//      Verified against the matching .sha256 sidecar, chmod +x,
//      stashed in ~/.tape/bin/, then exec'd. Subsequent runs hit
//      path 2 above with zero network.
// PACKAGE_NAME and PACKAGE_VERSION are rewritten by
// scripts/release-npm.sh at publish time. We don't read them from
// require('./package.json') because then a global install in an
// odd npm layout (yarn-pnp, pnpm strict, corepack) can't find
// itself — string-replaced constants are bulletproof.
"use strict";

const { spawnSync, execSync } = require("child_process");
const fs = require("fs");
const os = require("os");
const path = require("path");

const PACKAGE_NAME = "@tapeai/tape";
const PACKAGE_VERSION = "__VERSION__";

const PLATFORMS = {
  "linux-x64": { pkg: "linux-x64", asset: "tape-linux-amd64" },
  "linux-arm64": { pkg: "linux-arm64", asset: "tape-linux-arm64" },
  "darwin-x64": { pkg: "darwin-x64", asset: "tape-darwin-amd64" },
  "darwin-arm64": { pkg: "darwin-arm64", asset: "tape-darwin-arm64" },
  "win32-x64": { pkg: "win32-x64", asset: "tape-windows-amd64.exe" },
};

const MIRRORS = [
  {
    name: "github",
    base: `https://github.com/chenhg5/tape/releases/download/v${PACKAGE_VERSION}`,
  },
  {
    name: "gitee",
    base: `https://gitee.com/cg33/tape/releases/download/v${PACKAGE_VERSION}`,
  },
];

function key() {
  return `${process.platform}-${process.arch}`;
}

function platformOrDie() {
  const k = key();
  const p = PLATFORMS[k];
  if (!p) {
    console.error(`tape: unsupported platform ${k}.`);
    console.error(
      "Build from source: go install github.com/chenhg5/tape/cmd/tape@latest"
    );
    process.exit(1);
  }
  return p;
}

function resolvePackaged(p) {
  const bin = process.platform === "win32" ? "tape.exe" : "tape";
  try {
    return require.resolve(`${PACKAGE_NAME}-${p.pkg}/bin/${bin}`);
  } catch {
    return null;
  }
}

function cachePath(p) {
  const home = process.env.TAPE_HOME || path.join(os.homedir(), ".tape");
  const dir = path.join(home, "bin");
  const name = `tape-${PACKAGE_VERSION}${process.platform === "win32" ? ".exe" : ""}`;
  return { dir, file: path.join(dir, name) };
}

function resolveCached(p) {
  const { file } = cachePath(p);
  return fs.existsSync(file) ? file : null;
}

// Synchronous curl wrapper. We deliberately shell out to curl
// (and PowerShell on Windows) instead of using node:https because:
//   - the fallback only runs once per install, the perf cost of a
//     subprocess is invisible against a multi-MB network download
//   - curl handles HTTP/2, redirects, sni quirks, system CA bundle,
//     and the corporate-proxy env vars (http_proxy / https_proxy)
//     without us reimplementing any of it
//   - the alternative (node:https + sync await via deasync or
//     spawnSync('node', ['-e', ...])) is meaningfully more code
//     and worse error messages for the user
// On Windows we use PowerShell's Invoke-WebRequest, which is
// preinstalled on every supported Windows version.
function downloadSync(url, dest, timeoutSec) {
  if (process.platform === "win32") {
    execSync(
      `powershell -NoProfile -Command "$ProgressPreference='SilentlyContinue'; ` +
        `Invoke-WebRequest -UseBasicParsing -TimeoutSec ${timeoutSec} ` +
        `-Uri '${url}' -OutFile '${dest}'"`,
      { stdio: ["ignore", "ignore", "pipe"] }
    );
  } else {
    execSync(
      `curl -fsSL --max-time ${timeoutSec} -o '${dest}' '${url}'`,
      { stdio: ["ignore", "ignore", "pipe"] }
    );
  }
}

function sha256File(file) {
  const buf = fs.readFileSync(file);
  return require("crypto").createHash("sha256").update(buf).digest("hex");
}

// Probe each mirror's HEAD request, fastest non-error wins.
// We can't do real concurrency in a synchronous launcher, so we
// settle for sequential probes with a tight (1.5s) timeout each —
// in the bad case (both unreachable) the user waits 3s before the
// real error. In the good case (GH responds) we waste 0ms past
// the GH probe.
function pickMirror(p) {
  const envPin = (process.env.TAPE_MIRROR || "").toLowerCase();
  const ordered = envPin
    ? MIRRORS.slice().sort((a, b) => (a.name === envPin ? -1 : 1))
    : MIRRORS;
  for (const m of ordered) {
    const url = `${m.base}/${p.asset}.sha256`;
    try {
      if (process.platform === "win32") {
        execSync(
          `powershell -NoProfile -Command "$ProgressPreference='SilentlyContinue'; ` +
            `try { (Invoke-WebRequest -UseBasicParsing -Method Head ` +
            `-TimeoutSec 2 -Uri '${url}').StatusCode } catch { exit 1 }"`,
          { stdio: ["ignore", "ignore", "pipe"] }
        );
      } else {
        execSync(`curl -fsI --max-time 2 -o /dev/null '${url}'`, {
          stdio: ["ignore", "ignore", "pipe"],
        });
      }
      return m;
    } catch {
      continue;
    }
  }
  return null;
}

function fetchAndCache(p) {
  const { dir, file } = cachePath(p);
  fs.mkdirSync(dir, { recursive: true });
  const m = pickMirror(p);
  if (!m) {
    console.error(
      "tape: could not reach GitHub or Gitee to fetch the binary."
    );
    console.error(
      "Check your network, or pin a mirror with TAPE_MIRROR=gitee, " +
        "or build from source: go install github.com/chenhg5/tape/cmd/tape@latest"
    );
    process.exit(1);
  }
  const url = `${m.base}/${p.asset}`;
  const shaUrl = `${url}.sha256`;
  const tmp = `${file}.partial`;
  const tmpSha = `${file}.sha256`;
  process.stderr.write(
    `tape: fetching prebuilt binary from ${m.name} (${PACKAGE_VERSION})... `
  );
  try {
    downloadSync(url, tmp, 60);
    downloadSync(shaUrl, tmpSha, 10);
    const expected = fs.readFileSync(tmpSha, "utf8").split(/\s+/)[0];
    const got = sha256File(tmp);
    if (expected !== got) {
      throw new Error(`sha256 mismatch (expected ${expected}, got ${got})`);
    }
    if (process.platform !== "win32") fs.chmodSync(tmp, 0o755);
    fs.renameSync(tmp, file);
    fs.unlinkSync(tmpSha);
    process.stderr.write("ok\n");
    return file;
  } catch (e) {
    process.stderr.write("failed\n");
    try { fs.unlinkSync(tmp); } catch {}
    try { fs.unlinkSync(tmpSha); } catch {}
    console.error(`tape: download from ${m.name} failed: ${e.message}`);
    console.error(
      "Fixes:\n" +
        `  npm install -g ${PACKAGE_NAME} --force\n` +
        "  TAPE_MIRROR=gitee tape ...        # pin the other mirror\n" +
        "  go install github.com/chenhg5/tape/cmd/tape@latest"
    );
    process.exit(1);
  }
}

function resolveBinary() {
  const p = platformOrDie();
  return (
    resolvePackaged(p) ||
    resolveCached(p) ||
    fetchAndCache(p)
  );
}

const result = spawnSync(resolveBinary(), process.argv.slice(2), {
  stdio: "inherit",
});
if (result.error) {
  console.error(`tape: ${result.error.message}`);
  process.exit(1);
}
process.exit(result.status === null ? 1 : result.status);
