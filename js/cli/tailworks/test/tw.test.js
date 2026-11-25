// The shim finds this platform's @tailworks/cli-* package and runs its
// bin/tw with the arguments, stdio and exit status passed through.
"use strict";
const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const { spawnSync } = require("node:child_process");

const TARGETS = { "darwin-arm64": "darwin-arm64", "linux-arm64": "linux-arm64", "linux-x64": "linux-amd64" };
const target = TARGETS[`${process.platform}-${process.arch}`];
const shim = path.join(__dirname, "..", "bin", "tw.js");

// layout makes node_modules/{tailworks,@tailworks/cli-<target>} as npm lays
// them out (hoisted), with a fake bin/tw; it returns the installed shim.
function layout(withPlatform) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), "tw-shim-"));
  const nm = path.join(root, "node_modules");
  fs.mkdirSync(path.join(nm, "tailworks", "bin"), { recursive: true });
  fs.copyFileSync(shim, path.join(nm, "tailworks", "bin", "tw.js"));
  if (withPlatform) {
    const p = path.join(nm, "@tailworks", `cli-${target}`);
    fs.mkdirSync(path.join(p, "bin"), { recursive: true });
    fs.writeFileSync(path.join(p, "package.json"), JSON.stringify({ name: `@tailworks/cli-${target}`, version: "0.0.0" }));
    fs.writeFileSync(path.join(p, "bin", "tw"), '#!/bin/sh\necho "tw $*"\n[ "$1" = fail ] && exit 7\nexit 0\n', { mode: 0o755 });
  }
  return path.join(nm, "tailworks", "bin", "tw.js");
}

test("runs the platform package's tw", { skip: !target && "unsupported platform" }, () => {
  const tw = layout(true);
  let r = spawnSync(process.execPath, [tw, "dev", "--port", "0"], { encoding: "utf8" });
  assert.strictEqual(r.status, 0, r.stderr);
  assert.strictEqual(r.stdout, "tw dev --port 0\n");
  r = spawnSync(process.execPath, [tw, "fail"], { encoding: "utf8" });
  assert.strictEqual(r.status, 7, "exit status passed through");
});

test("says what's missing without the platform package", { skip: !target && "unsupported platform" }, () => {
  const r = spawnSync(process.execPath, [layout(false)], { encoding: "utf8" });
  assert.strictEqual(r.status, 1);
  assert.match(r.stderr, new RegExp(`@tailworks/cli-${target} is not installed`));
  assert.match(r.stderr, /--omit=optional/);
});

test("optionalDependencies name every release target at the package's version", () => {
  const p = JSON.parse(fs.readFileSync(path.join(__dirname, "..", "package.json"), "utf8"));
  assert.deepStrictEqual(Object.keys(p.optionalDependencies).sort(), Object.values(TARGETS).map((t) => `@tailworks/cli-${t}`).sort());
  for (const v of Object.values(p.optionalDependencies)) assert.strictEqual(v, p.version);
  assert.deepStrictEqual(Object.keys(p.bin).sort(), ["tailworks", "tw"]);
});
