#!/usr/bin/env node
// tw (and tailworks): run the tailworks CLI from the platform package npm
// installed for this machine (issue #240).
//
// The binaries come in one package per release target,
// @tailworks/cli-<os>-<arch>, listed as optionalDependencies with "os" and
// "cpu" set, so npm installs only this machine's. That package holds the
// release tarball's layout (bin/tw, the daemons, workerd, share/tailworks
// with the templates and their vendored modules), so tw finds everything
// next to itself. This shim only finds the package and runs its bin/tw:
// no install script, no download.
"use strict";

const { spawn } = require("node:child_process");
const fs = require("node:fs");
const path = require("node:path");

// node's platform-arch → the release target (npm and node call amd64 "x64").
const TARGETS = {
  "darwin-arm64": "darwin-arm64",
  "linux-arm64": "linux-arm64",
  "linux-x64": "linux-amd64",
};

function die(msg) {
  process.stderr.write(`tailworks: ${msg}\n`);
  process.exit(1);
}

const key = `${process.platform}-${process.arch}`;
const target = TARGETS[key];
if (!target) {
  let hint = "tailworks runs on macOS (Apple silicon) and Linux (arm64, amd64, glibc >= 2.35)";
  if (key === "darwin-x64") hint += "; on an Apple silicon Mac, use an arm64 Node.js (this one runs under Rosetta)";
  die(`no tailworks build for ${key}: ${hint}`);
}
const pkg = `@tailworks/cli-${target}`;

let dir;
try {
  dir = path.dirname(require.resolve(`${pkg}/package.json`));
} catch {
  die(
    `${pkg} is not installed. npm installs it with tailworks (an optional dependency for this platform), unless it ran with --omit=optional or --no-optional, or this Linux isn't glibc (musl/Alpine can't run tailworks' workerd).\n` +
      `  reinstall: npm install -g tailworks   (or npx tailworks@latest …)\n` +
      `  or install the release tarball: https://github.com/rosskukulinski/tailworks/blob/master/docs/INSTALL.md`,
  );
}
const tw = path.join(dir, "bin", "tw");
try {
  fs.accessSync(tw, fs.constants.X_OK);
} catch {
  die(`${tw} is missing or not executable: reinstall tailworks (npm install -g tailworks)`);
}

const child = spawn(tw, process.argv.slice(2), { stdio: "inherit" });
child.on("error", (err) => die(`can't run ${tw}: ${err.message}`));

// Ctrl-C and Ctrl-\ reach tw from the terminal (same process group); tw
// decides what they mean, so the shim ignores them rather than exiting
// under it. Signals sent to the shim alone (kill, a process manager) are
// passed on.
process.on("SIGINT", () => {});
process.on("SIGQUIT", () => {});
for (const sig of ["SIGTERM", "SIGHUP"]) {
  process.on(sig, () => {
    if (child.exitCode === null) child.kill(sig);
  });
}
child.on("exit", (code, signal) => {
  if (signal) {
    // Exit the same way tw did, so callers see the signal.
    process.removeAllListeners(signal);
    process.kill(process.pid, signal);
    return;
  }
  process.exit(code ?? 1);
});
