// ci-npm-overrides.mjs <app package.json> <dir of npm pack tarballs>: point
// an app's @tailworks/* dependencies (and npm overrides) at local `npm pack`
// tarballs, so it installs this checkout's packages before they are on npm
// (scripts/ci-tarball-check.sh; scripts/templates-typecheck.sh does the same).
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";

const [file, pkgs] = process.argv.slice(2);
if (!file || !pkgs) {
  console.error("usage: ci-npm-overrides.mjs <package.json> <tarball dir>");
  process.exit(2);
}
const p = JSON.parse(fs.readFileSync(file, "utf8"));
p.overrides = {};
for (const tgz of fs.readdirSync(pkgs).filter((f) => f.endsWith(".tgz"))) {
  const abs = path.resolve(pkgs, tgz);
  const name = JSON.parse(execFileSync("tar", ["-xzOf", abs, "package/package.json"])).name;
  const spec = "file:" + abs;
  for (const sec of ["dependencies", "devDependencies"]) if (p[sec]?.[name]) p[sec][name] = spec;
  p.overrides[name] = spec;
}
fs.writeFileSync(file, JSON.stringify(p, null, 2) + "\n");
