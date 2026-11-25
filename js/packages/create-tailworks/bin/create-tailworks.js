#!/usr/bin/env node
// create-tailworks: scaffold a tailworks app from a template, or bundle one.
//   npm create tailworks@latest <dir> -- --template hello|agent|mcp|ai-agent [--name <workload>]
//   create-tailworks build [dir]        (bundles wrangler.jsonc main → dist/index.js)
//   create-tailworks build-node [dir] [outdir]  (runtime "node" apps: dist/server.mjs + Workflow bundles)
import { cpSync, existsSync, readdirSync, renameSync } from "node:fs";
import { spawnSync } from "node:child_process";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const templates = path.join(here, "..", "templates");
const args = process.argv.slice(2);

if (args[0] === "build") {
  const { buildApp } = await import("../lib/build.mjs");
  const r = await buildApp(path.resolve(args[1] ?? "."));
  console.log(`built ${r.outfile} (${r.bytes} bytes)`);
  process.exit(0);
}

if (args[0] === "build-node") {
  const { buildNodeApp } = await import("../lib/build-node.mjs");
  const dir = path.resolve(args[1] ?? ".");
  const r = await buildNodeApp(dir, path.resolve(args[2] ?? path.join(dir, ".tailworks-build")));
  console.log(`built ${r.outdir}/${r.main} (${r.bytes} bytes${r.workflows ? ", with Vercel Workflow bundles" : ""})`);
  process.exit(0);
}

// The flags tw init takes; create-tailworks passes them on to it. --flag
// value or --flag=value, so a flag's value is never taken as the directory.
const valueFlags = new Set(["template", "name", "tags", "object-tags"]);
const opts = { "object-tags": [] };
const positional = [];
for (let i = 0; i < args.length; i++) {
  const m = args[i].match(/^--?([a-z-]+)(?:=(.*))?$/);
  if (!m) { positional.push(args[i]); continue; }
  const [, flag, inline] = m;
  if (!valueFlags.has(flag)) {
    console.error(`create-tailworks: unknown flag --${flag} (want --template, --name, --tags, --object-tags)`);
    process.exit(2);
  }
  const v = inline ?? args[++i];
  if (v === undefined) {
    console.error(`create-tailworks: --${flag} needs a value`);
    process.exit(2);
  }
  if (flag === "object-tags") opts[flag].push(v);
  else opts[flag] = v;
}
const template = opts.template ?? "hello";
const dir = positional[0] ?? template;
const available = readdirSync(templates);
if (!available.includes(template)) {
  console.error(`create-tailworks: unknown template "${template}" (available: ${available.join(", ")})`);
  process.exit(2);
}
if (existsSync(dir) && readdirSync(dir).length) {
  console.error(`create-tailworks: ${dir} exists and is not empty`);
  process.exit(2);
}
// `npm create tailworks`: with the tailworks CLI installed (the release
// tarball, or npm install -g tailworks), `tw init` does it; without it, copy
// the template and rename it the way tw init does (lib/init.mjs).
const twArgs = ["init", dir, "--template", template];
if (opts.name !== undefined) twArgs.push("--name", opts.name);
if (opts.tags !== undefined) twArgs.push("--tags", opts.tags);
for (const v of opts["object-tags"]) twArgs.push("--object-tags", v);
const tw = spawnSync("tw", twArgs, { stdio: "inherit" });
if (!tw.error) process.exit(tw.status ?? 1);
// Tags are the grant unit: don't scaffold without the ones asked for.
if (opts.tags !== undefined || opts["object-tags"].length) {
  console.error("create-tailworks: --tags and --object-tags need the tailworks CLI (tw is not on PATH)");
  console.error(`  run: npx tailworks ${twArgs.map((a) => (/^[\w.:,=@/-]+$/.test(a) ? a : JSON.stringify(a))).join(" ")}`);
  process.exit(2);
}
const { renameApp, workloadLabel } = await import("../lib/init.mjs");
const wl = workloadLabel(opts.name ?? path.basename(path.resolve(dir)));
if (!wl) {
  console.error(`create-tailworks: can't derive a workload name from ${dir}; pass --name <workload>`);
  process.exit(2);
}
cpSync(path.join(templates, template), dir, { recursive: true });
// npm drops .gitignore from published packages: templates ship "gitignore".
if (existsSync(path.join(dir, "gitignore"))) renameSync(path.join(dir, "gitignore"), path.join(dir, ".gitignore"));
renameApp(dir, wl);
// The same two next steps tw init prints (cmd/tw/templates.go), in their
// npx forms.
const cd = `cd ${dir} && ${template === "node-workflow" ? "npm install && " : ""}npx tailworks dev`;
console.log(`created ${dir} from template ${template} (workload "${wl}")`);
console.log(`  ${cd.padEnd(40)} # try it here, no tailnet`);
console.log(`  ${"npx tailworks deploy --apply-policy".padEnd(40)} # shows the policy lines, asks, writes, deploys`);
if (template === "ai-agent") console.log(`  its tools need the mcp template deployed as workload "mcp" and a grant: see ${path.join(dir, "README.md")}`);
