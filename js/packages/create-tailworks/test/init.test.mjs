// `npm create tailworks` without tw on PATH (bin/create-tailworks.js,
// lib/init.mjs). cmd/tw's TestCreateTailworksRenamesLikeInit diffs every
// template against tw init; these cover the flag handling.
import { test } from "node:test";
import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, symlinkSync } from "node:fs";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { workloadLabel } from "../lib/init.mjs";

const bin = fileURLToPath(new URL("../bin/create-tailworks.js", import.meta.url));

// create runs create-tailworks in a fresh directory with only node on PATH.
function create(...args) {
  const cwd = mkdtempSync(path.join(tmpdir(), "ctw-"));
  const pathDir = mkdtempSync(path.join(tmpdir(), "ctw-bin-"));
  symlinkSync(process.execPath, path.join(pathDir, "node"));
  const r = spawnSync(process.execPath, [bin, ...args], { cwd, env: { ...process.env, PATH: pathDir }, encoding: "utf8" });
  return { ...r, cwd };
}

test("workloadLabel matches tw init's", () => {
  assert.equal(workloadLabel("My_App--2-"), "my-app-2");
  assert.equal(workloadLabel("\u0130stanbul_App"), "istanbul-app"); // Go lowercases \u0130 to i
  assert.equal(workloadLabel("a".repeat(39) + "-bc"), "a".repeat(39));
});

test("a flag's value is not taken as the directory; the app is named after it", () => {
  const r = create("--template", "agent", "my-agent");
  assert.equal(r.status, 0, r.stderr);
  assert.match(r.stdout, /created my-agent from template agent \(workload "my-agent"\)/);
  const tw = readFileSync(path.join(r.cwd, "my-agent", "tailworks.jsonc"), "utf8");
  assert.match(tw, /"tag:tw-wl-my-agent"/);
  assert.match(tw, /"tag:tw-my-agent-support-agent"/);
  assert.match(readFileSync(path.join(r.cwd, "my-agent", "wrangler.jsonc"), "utf8"), /"name": "my-agent"/);
});

test("--name and --template=", () => {
  const r = create("app", "--template=hello", "--name=Shop");
  assert.equal(r.status, 0, r.stderr);
  assert.match(readFileSync(path.join(r.cwd, "app", "wrangler.jsonc"), "utf8"), /"name": "shop"/);
});

test("--tags without tw refuses rather than drop the tags", () => {
  const r = create("app", "--template", "hello", "--tags", "tag:tw-app");
  assert.equal(r.status, 2);
  assert.match(r.stderr, /need the tailworks CLI/);
  assert.match(r.stderr, /npx tailworks init app --template hello --tags tag:tw-app/);
  assert.equal(existsSync(path.join(r.cwd, "app")), false);
});
