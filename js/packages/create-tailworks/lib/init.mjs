// renameApp is `tw init`'s renaming (cmd/tw/app.go cmdInit, cmd/tw/inittags.go
// appTags) for `npm create tailworks` without the tw CLI on PATH: it names the
// scaffolded app's workload, npm package and tags after the directory, so two
// apps made from one template don't share a name or tags (and with them every
// grant). Keep the two in step: cmd/tw/createtailworks_test.go diffs this
// against tw init for every template.
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { stripJSONC } from "./build.mjs";

// workloadLabel is cmd/tw/app.go's: lowercase, runs of anything but
// [a-z0-9-] to "-", trimmed, at most 40 characters.
export function workloadLabel(s) {
  s = s.toLowerCase().replace(/[^a-z0-9-]+/g, "-").replace(/-+/g, "-").replace(/^-+|-+$/g, "");
  if (s.length > 40) s = s.slice(0, 40).replace(/-+$/, "");
  return s;
}

// classLabel is inittags.go's: SupportAgent -> support-agent.
function classLabel(c) {
  return workloadLabel(c.replace(/([a-z0-9])([A-Z])/g, "$1-$2"));
}

// appTags is inittags.go's: tailnet.tags -> tag:tw-wl-<wl>, each class's
// tags -> tag:tw-<wl>-<class>; only tailworks' own "tag:tw-…" are renamed.
function appTags(raw, wl) {
  let cfg;
  try {
    cfg = JSON.parse(stripJSONC(raw));
  } catch {
    return raw;
  }
  const tn = cfg?.tailnet ?? {};
  const rename = new Map();
  const add = (old, nu) => {
    if (typeof old === "string" && old.startsWith("tag:tw-") && old !== nu) rename.set(old, nu);
  };
  for (const t of tn.tags ?? []) add(t, `tag:tw-wl-${wl}`);
  for (const c of Object.keys(tn.objects ?? {}).sort()) {
    for (const t of tn.objects[c]?.tags ?? []) if (!rename.has(t)) add(t, `tag:tw-${wl}-${classLabel(c)}`);
  }
  for (const [old, nu] of rename) raw = raw.split(`"${old}"`).join(`"${nu}"`);
  return raw;
}

// renameApp renames the app scaffolded in dir after wl.
export function renameApp(dir, wl) {
  for (const fn of ["package.json", "tailworks.jsonc", "wrangler.jsonc"]) {
    const p = path.join(dir, fn);
    if (!existsSync(p)) continue;
    // The first "name" (package.json's top-level one; the workload's in the others).
    let b = readFileSync(p, "utf8").replace(/("name"\s*:\s*)"[^"]*"/, `$1"${wl}"`);
    if (fn === "tailworks.jsonc") {
      b = appTags(b, wl);
      const m = b.match(/"tags"\s*:\s*\[\s*"(tag:tw-wl-[A-Za-z0-9._-]+)"/);
      if (m && m[1] !== `tag:tw-wl-${wl}`) b = b.split(m[1]).join(`tag:tw-wl-${wl}`);
    }
    writeFileSync(p, b);
  }
}
