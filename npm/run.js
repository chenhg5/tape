#!/usr/bin/env node
// Thin launcher: exec the prebuilt binary in ./bin/. If it's
// missing, broken, or for a different version than the npm package
// expects (postinstall failed, npm i --ignore-scripts, sub-shell
// after a git clone), trigger install.js to self-heal, then exec.
//
// We deliberately re-run install.js (synchronous, single-shot)
// instead of inlining download logic here, because install.js
// already handles mirror race + sha256 verification + chmod +
// quarantine clear correctly. Two code paths to maintain is
// strictly worse than one.
"use strict";

const fs = require("fs");
const path = require("path");
const { spawnSync, execFileSync } = require("child_process");

const PKG = require("./package.json");
const VERSION = PKG.version;
const NAME = "tape";

const binDir = path.join(__dirname, "bin");
const binFile = path.join(
  binDir,
  process.platform === "win32" ? `${NAME}.exe` : NAME
);

function needsReinstall() {
  if (!fs.existsSync(binFile)) return true;
  try {
    const out = execFileSync(binFile, ["version", "--json"], {
      encoding: "utf8",
      timeout: 5_000,
    });
    const data = JSON.parse(out).data || {};
    // Hard match: the npm package's expected version is the truth.
    // If the binary on disk is older or newer than what npm shipped,
    // re-fetch so the launcher and binary always agree.
    return data.version !== VERSION;
  } catch {
    return true;
  }
}

if (needsReinstall()) {
  process.stderr.write(
    `[tape] binary missing or version mismatch, fetching v${VERSION}...\n`
  );
  const r = spawnSync("node", [path.join(__dirname, "install.js")], {
    stdio: "inherit",
    cwd: __dirname,
  });
  if (r.status !== 0 || !fs.existsSync(binFile)) {
    process.stderr.write(
      `[tape] auto-install failed. Try:\n` +
        `  npm install -g @tapeai/tape --force\n` +
        `  TAPE_MIRROR=gitee npm install -g @tapeai/tape   # CN users\n` +
        `  go install github.com/chenhg5/tape/cmd/tape@latest\n`
    );
    process.exit(1);
  }
}

const result = spawnSync(binFile, process.argv.slice(2), { stdio: "inherit" });
if (result.error) {
  process.stderr.write(`[tape] ${result.error.message}\n`);
  process.exit(1);
}
process.exit(result.status === null ? 1 : result.status);
