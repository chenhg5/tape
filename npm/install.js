#!/usr/bin/env node
// postinstall: fetch the right prebuilt tape binary for this OS/arch
// from a release mirror, verify its sha256, drop it in ./bin/, mark
// it executable, and clear the macOS quarantine bit.
//
// Mirror strategy: race a 2s HEAD probe against GitHub and Gitee in
// parallel — whichever answers first wins the actual download. CN
// users behind a throttled github.com automatically land on Gitee
// with zero configuration. Override with TAPE_MIRROR={github,gitee}.
//
// Failure policy: if the download fails (offline install, locked-down
// CI, dead mirror), exit 0 with a loud warning instead of failing the
// npm install. run.js will retry the fetch on the first `tape ...`
// invocation, so the user has a chance to fix their network without
// having to `npm install -g` a second time.
"use strict";

const fs = require("fs");
const os = require("os");
const path = require("path");
const https = require("https");
const http = require("http");
const crypto = require("crypto");
const { execSync } = require("child_process");

const PKG = require("./package.json");
const VERSION = PKG.version;
const NAME = "tape";

const MIRRORS = [
  { name: "github", base: `https://github.com/chenhg5/tape/releases/download/v${VERSION}` },
  { name: "gitee",  base: `https://gitee.com/cg33/tape/releases/download/v${VERSION}` },
];

const PLATFORM_MAP = { darwin: "darwin", linux: "linux", win32: "windows" };
const ARCH_MAP = { x64: "amd64", arm64: "arm64" };

function platformOrDie() {
  const platform = PLATFORM_MAP[process.platform];
  const arch = ARCH_MAP[process.arch];
  if (!platform || !arch) {
    throw new Error(
      `unsupported platform ${process.platform}/${process.arch}. ` +
        `supported: linux/darwin/windows on x64/arm64`
    );
  }
  const ext = platform === "windows" ? ".exe" : "";
  const asset = `${NAME}-${platform}-${arch}${ext}`;
  const binaryName = platform === "windows" ? `${NAME}.exe` : NAME;
  return { platform, arch, asset, binaryName, isWin: platform === "windows" };
}

// Minimal HTTP(S) GET with redirect following, returning the
// response body as a Buffer. We use node's stdlib instead of curl /
// wget so the install works on any platform with Node — including
// stock Windows and locked-down container images that ship without
// curl.
function fetchBuf(url, { timeoutMs, redirects = 5 } = {}) {
  return new Promise((resolve, reject) => {
    if (redirects <= 0) return reject(new Error("too many redirects"));
    const mod = url.startsWith("https") ? https : http;
    const req = mod.get(
      url,
      { headers: { "User-Agent": `tape-npm/${VERSION}` } },
      (res) => {
        if (res.statusCode >= 300 && res.statusCode < 400 && res.headers.location) {
          res.resume();
          return resolve(
            fetchBuf(new URL(res.headers.location, url).toString(), {
              timeoutMs,
              redirects: redirects - 1,
            })
          );
        }
        if (res.statusCode !== 200) {
          res.resume();
          return reject(new Error(`HTTP ${res.statusCode} for ${url}`));
        }
        const chunks = [];
        res.on("data", (c) => chunks.push(c));
        res.on("end", () => resolve(Buffer.concat(chunks)));
        res.on("error", reject);
      }
    );
    req.on("error", reject);
    if (timeoutMs) {
      req.setTimeout(timeoutMs, () => req.destroy(new Error(`timeout after ${timeoutMs}ms`)));
    }
  });
}

// HEAD probe: just check the URL is reachable. We don't care about
// the body — we use this to race the mirrors and pick the fastest.
function probeHead(url, timeoutMs) {
  return new Promise((resolve, reject) => {
    const mod = url.startsWith("https") ? https : http;
    const req = mod.request(
      url,
      { method: "HEAD", headers: { "User-Agent": `tape-npm/${VERSION}` } },
      (res) => {
        res.resume();
        // 200, 30x, even 405 (Gitee sometimes rejects HEAD but the
        // GET works) → mirror is alive
        if (res.statusCode < 500) return resolve();
        reject(new Error(`HTTP ${res.statusCode}`));
      }
    );
    req.on("error", reject);
    req.setTimeout(timeoutMs, () => req.destroy(new Error("timeout")));
    req.end();
  });
}

async function pickMirror(asset) {
  const envPin = (process.env.TAPE_MIRROR || "").toLowerCase();
  if (envPin) {
    const pinned = MIRRORS.find((m) => m.name === envPin);
    if (pinned) {
      console.log(`[tape] mirror pinned via TAPE_MIRROR: ${pinned.name}`);
      return pinned;
    }
    console.warn(`[tape] TAPE_MIRROR=${envPin} unrecognized, racing all mirrors`);
  }
  // Race: first mirror to answer HEAD wins. Promise.any resolves
  // with the first fulfilled; if both reject, it throws
  // AggregateError. Node 18+ supports both.
  try {
    const winner = await Promise.any(
      MIRRORS.map((m) =>
        probeHead(`${m.base}/${asset}`, 2500).then(() => m)
      )
    );
    console.log(`[tape] mirror chosen: ${winner.name}`);
    return winner;
  } catch (e) {
    throw new Error(
      `no mirror reachable (tried ${MIRRORS.map((m) => m.name).join(", ")})`
    );
  }
}

async function downloadAndVerify(mirror, asset, destFile) {
  const url = `${mirror.base}/${asset}`;
  const shaUrl = `${url}.sha256`;
  console.log(`[tape] downloading ${asset} (${VERSION}) from ${mirror.name}`);
  const [bin, sha] = await Promise.all([
    fetchBuf(url, { timeoutMs: 60_000 }),
    fetchBuf(shaUrl, { timeoutMs: 10_000 }),
  ]);
  const expected = sha.toString("utf8").trim().split(/\s+/)[0];
  const got = crypto.createHash("sha256").update(bin).digest("hex");
  if (expected !== got) {
    throw new Error(`sha256 mismatch: expected ${expected}, got ${got}`);
  }
  fs.writeFileSync(destFile, bin);
  console.log(`[tape] verified sha256 (${(bin.length / 1024 / 1024).toFixed(1)} MB)`);
}

function clearMacOSQuarantine(file) {
  if (process.platform !== "darwin") return;
  try {
    execSync(`xattr -d com.apple.quarantine "${file}"`, { stdio: "pipe" });
  } catch {
    // xattr returns non-zero when the attribute isn't set; that's
    // the normal case for binaries downloaded via node:https.
  }
}

async function main() {
  const p = platformOrDie();
  console.log(`[tape] platform: ${p.platform}/${p.arch}`);

  const binDir = path.join(__dirname, "bin");
  const binFile = path.join(binDir, p.binaryName);
  fs.mkdirSync(binDir, { recursive: true });

  // If an existing binary matches our version, do nothing. This
  // covers the rare case where postinstall fires twice (e.g. yarn
  // upgrade) — no point re-downloading.
  if (fs.existsSync(binFile)) {
    try {
      const out = execSync(`"${binFile}" version --json`, {
        encoding: "utf8",
        timeout: 5_000,
      });
      const data = JSON.parse(out).data || {};
      if (data.version === VERSION) {
        console.log(`[tape] binary ${VERSION} already present, skipping download`);
        return;
      }
    } catch {
      // existing binary is broken / unparseable; we'll overwrite
    }
  }

  const mirror = await pickMirror(p.asset);
  await downloadAndVerify(mirror, p.asset, binFile);
  if (!p.isWin) fs.chmodSync(binFile, 0o755);
  clearMacOSQuarantine(binFile);
  console.log(`[tape] installed at ${binFile}`);
}

main().catch((err) => {
  // Loud, but exit 0 — don't break `npm install -g`. run.js will
  // retry on first invocation, so a flaky network at install time
  // becomes a one-shot self-heal at first `tape ...`.
  console.warn(`[tape] postinstall: ${err.message}`);
  console.warn(`[tape] binary will be fetched on first invocation`);
  console.warn(
    `[tape] manual install:\n` +
      `       https://github.com/chenhg5/tape/releases/tag/v${VERSION}\n` +
      `       https://gitee.com/cg33/tape/releases/tag/v${VERSION}`
  );
  process.exit(0);
});
