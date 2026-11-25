#!/usr/bin/env node
// npm-versions.mjs [--set] [version]: the npm side of "one version for
// everything" (ADR-041 §1, docs/RELEASING.md).
//
//   node scripts/npm-versions.mjs            check against VERSION
//   node scripts/npm-versions.mjs 0.3.0      check against 0.3.0 (the release tag)
//   node scripts/npm-versions.mjs --set 0.3.0  rewrite every version below to 0.3.0
//
// Checked (and set):
//   - every workspace package (js/packages/*: @tailworks/*, create-tailworks) is at the
//     version, and one workspace package depends on another at exactly it;
//   - the tw CLI's package (js/cli/tailworks) is at the version, and so is
//     each platform package it lists (@tailworks/cli-<os>-<arch>);
//   - every create-tailworks template depends on the workspace packages at
//     "^<version>", the range `npm install` resolves after a release;
//   - the published packages carry what npm needs: name, license,
//     repository, publishConfig.access "public", and "files".
// Edits are textual, so a --set diff is the version strings only.
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const args = process.argv.slice(2);
const set = args[0] === "--set";
if (set) args.shift();
const want = (args[0] ?? fs.readFileSync(path.join(root, "VERSION"), "utf8")).trim().replace(/^v/, "");
if (!/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(want)) {
  console.error(`npm-versions: ${JSON.stringify(want)} is not a version`);
  process.exit(2);
}

const pkgDir = path.join(root, "js", "packages");
const workspaces = new Map(); // name -> file
for (const d of fs.readdirSync(pkgDir).sort()) {
  const f = path.join(pkgDir, d, "package.json");
  if (!fs.existsSync(f)) continue;
  workspaces.set(JSON.parse(fs.readFileSync(f, "utf8")).name, f);
}
const tmplDir = path.join(pkgDir, "create-tailworks", "templates");
const templates = fs
  .readdirSync(tmplDir)
  .sort()
  .map((t) => path.join(tmplDir, t, "package.json"))
  .filter((f) => fs.existsSync(f));

const problems = []; // fixable by --set
const meta = []; // not fixable by --set
const rel = (f) => path.relative(root, f);
const esc = (s) => s.replace(/[.*+?^${}()|[\]\\/]/g, "\\$&");

// fix rewrites one "key": "value" (dependency or version) in the text.
function fix(text, key, value) {
  return text.replace(new RegExp(`("${esc(key)}"\\s*:\\s*)"[^"]*"`), `$1"${value}"`);
}

for (const [name, f] of workspaces) {
  let text = fs.readFileSync(f, "utf8");
  const p = JSON.parse(text);
  if (p.version !== want) {
    problems.push(`${rel(f)}: ${name} is at ${p.version}, want ${want}`);
    text = fix(text, "version", want);
  }
  for (const sec of ["dependencies", "peerDependencies", "optionalDependencies"]) {
    for (const [dep, v] of Object.entries(p[sec] ?? {})) {
      if (workspaces.has(dep) && v !== want) {
        problems.push(`${rel(f)}: ${sec}.${dep} is ${v}, want ${want} (published together, at one version)`);
        text = fix(text, dep, want);
      }
    }
  }
  if (!p.private) {
    for (const k of ["license", "repository", "files"]) {
      if (!p[k]) meta.push(`${rel(f)}: ${name} has no "${k}" (needed to publish)`);
    }
    if (p.publishConfig?.access !== "public") meta.push(`${rel(f)}: ${name} needs "publishConfig": { "access": "public" }`);
  }
  if (set) fs.writeFileSync(f, text);
}

// The tw CLI's npm package (js/cli/tailworks, issue #240): not a workspace
// (npm install would fetch its platform packages), but published at the
// same version, with every @tailworks/cli-<target> at exactly it.
{
  const f = path.join(root, "js", "cli", "tailworks", "package.json");
  let text = fs.readFileSync(f, "utf8");
  const p = JSON.parse(text);
  if (p.version !== want) {
    problems.push(`${rel(f)}: ${p.name} is at ${p.version}, want ${want}`);
    text = fix(text, "version", want);
  }
  const cli = Object.entries(p.optionalDependencies ?? {}).filter(([d]) => d.startsWith("@tailworks/cli-"));
  if (!cli.length) meta.push(`${rel(f)}: no @tailworks/cli-* optionalDependencies`);
  for (const [dep, v] of cli) {
    if (v !== want) {
      problems.push(`${rel(f)}: optionalDependencies.${dep} is ${v}, want ${want} (published together, at one version)`);
      text = fix(text, dep, want);
    }
  }
  for (const k of ["license", "repository", "files", "bin"]) {
    if (!p[k]) meta.push(`${rel(f)}: ${p.name} has no "${k}" (needed to publish)`);
  }
  if (set) fs.writeFileSync(f, text);
}

for (const f of templates) {
  let text = fs.readFileSync(f, "utf8");
  const p = JSON.parse(text);
  for (const sec of ["dependencies", "devDependencies"]) {
    for (const [dep, v] of Object.entries(p[sec] ?? {})) {
      if (workspaces.has(dep) && v !== `^${want}`) {
        problems.push(`${rel(f)}: ${sec}.${dep} is ${v}, want ^${want}`);
        text = fix(text, dep, `^${want}`);
      }
    }
  }
  if (set) fs.writeFileSync(f, text);
}

if (meta.length) {
  console.error(meta.join("\n"));
  console.error(`npm-versions: ${meta.length} package(s) can't be published as they are`);
  process.exit(1);
}
if (set) {
  console.log(`npm-versions: set ${workspaces.size} packages and ${templates.length} templates to ${want}` + (problems.length ? ` (${problems.length} changes)` : " (no changes)"));
  process.exit(0);
}
if (problems.length) {
  console.error(problems.join("\n"));
  console.error(`npm-versions: ${problems.length} problem(s); fix with: node scripts/npm-versions.mjs --set ${want}`);
  process.exit(1);
}
console.log(`npm-versions: ${workspaces.size} packages and ${templates.length} templates at ${want}`);
