#!/usr/bin/env node
// Thin launcher: resolves the prebuilt tape binary from the platform
// package that npm selected via optionalDependencies, and execs it.
// PACKAGE_NAME is rewritten by scripts/release-npm.sh at publish time.
"use strict";

const { spawnSync } = require("child_process");

const PACKAGE_NAME = "@tapeai/tape";

const PLATFORMS = {
  "linux-x64": `${PACKAGE_NAME}-linux-x64`,
  "linux-arm64": `${PACKAGE_NAME}-linux-arm64`,
  "darwin-x64": `${PACKAGE_NAME}-darwin-x64`,
  "darwin-arm64": `${PACKAGE_NAME}-darwin-arm64`,
  "win32-x64": `${PACKAGE_NAME}-win32-x64`,
};

function resolveBinary() {
  const key = `${process.platform}-${process.arch}`;
  const pkg = PLATFORMS[key];
  if (!pkg) {
    console.error(`tape: unsupported platform ${key}.`);
    console.error(
      "Build from source instead: go install github.com/chenhg5/tape/cmd/tape@latest"
    );
    process.exit(1);
  }
  const bin = process.platform === "win32" ? "tape.exe" : "tape";
  try {
    return require.resolve(`${pkg}/bin/${bin}`);
  } catch {
    console.error(`tape: prebuilt binary not found (package ${pkg}).`);
    console.error(
      "This usually means npm skipped optional dependencies. Try:\n" +
        `  npm install -g ${PACKAGE_NAME} --force\n` +
        "or build from source:\n" +
        "  go install github.com/chenhg5/tape/cmd/tape@latest"
    );
    process.exit(1);
  }
}

const result = spawnSync(resolveBinary(), process.argv.slice(2), {
  stdio: "inherit",
});
if (result.error) {
  console.error(`tape: ${result.error.message}`);
  process.exit(1);
}
// preserve tape's semantic exit codes (0/1/2/3/10)
process.exit(result.status === null ? 1 : result.status);
