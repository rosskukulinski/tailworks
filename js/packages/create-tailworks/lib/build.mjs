// buildApp bundles a tailworks app (wrangler.jsonc "main") into one ESM module at
// dist/index.js, the way wrangler does for Workers: workerd conditions, Node
// builtins mapped to node:* (needs "nodejs_compat"), cloudflare:* external.
// cloudflare:email is stubbed for dependencies and refused for the app's own
// code (email is not supported on tailworks).
import { builtinModules } from "node:module";
import { readFileSync, realpathSync } from "node:fs";
import path from "node:path";

const builtins = new Set(builtinModules);

export function stripJSONC(s) {
  let out = "", inStr = false, esc = false;
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    if (inStr) { out += c; if (esc) esc = false; else if (c === "\\") esc = true; else if (c === '"') inStr = false; continue; }
    if (c === '"') { inStr = true; out += c; }
    else if (c === "/" && s[i + 1] === "/") { while (i < s.length && s[i] !== "\n") i++; out += "\n"; }
    else if (c === "/" && s[i + 1] === "*") { i += 2; while (i < s.length && !(s[i] === "*" && s[i + 1] === "/")) i++; i++; }
    else out += c;
  }
  return out.replace(/,(\s*[}\]])/g, "$1");
}

export function readJSONC(file) {
  return JSON.parse(stripJSONC(readFileSync(file, "utf8")));
}

// requireBuiltinsJS is internal/esbundle/requirebuiltins.go: a require() of a
// Node builtin in a CommonJS dependency (dotenv's require("fs"), issue #356)
// becomes an import of it with nodejs_compat (workerd's ES modules have no
// require), and without it a module that throws, when required, an error
// naming the package and the flag. node:async_hooks always exists (nodejs_als).
const requireRE = /^(node:)?[a-z_][a-z0-9_/]*$/;

function importerPackage(dir, importer) {
  const p = importer.split(path.sep).join("/");
  const i = p.lastIndexOf("/node_modules/");
  if (i >= 0) {
    const parts = p.slice(i + "/node_modules/".length).split("/");
    return parts[0].startsWith("@") && parts.length > 1 ? parts[0] + "/" + parts[1] : parts[0];
  }
  const r = path.relative(dir, importer);
  return r.startsWith("..") ? p : r.split(path.sep).join("/");
}

function requireBuiltinsJS(dir, nodeCompat) {
  return {
    name: "tw-require",
    setup(b) {
      b.onResolve({ filter: requireRE }, (a) => {
        if (a.kind !== "require-call") return undefined;
        let name = "";
        if (a.path.startsWith("node:")) name = a.path.slice(5);
        else if (builtins.has(a.path.split("/")[0]) || builtins.has(a.path)) name = a.path;
        if (!name) return undefined;
        if (nodeCompat || name === "async_hooks") return { path: "node:" + name, namespace: "tw-require" };
        // One stub per package, so each names its own.
        return { path: "node:" + name + " in " + importerPackage(dir, a.importer), namespace: "tw-require-stub" };
      });
      b.onLoad({ filter: /.*/, namespace: "tw-require" }, (a) => ({
        contents: `import * as m from ${JSON.stringify(a.path)};\nmodule.exports = "default" in m ? m.default : m;\n`,
        loader: "js",
      }));
      b.onLoad({ filter: /.*/, namespace: "tw-require-stub" }, (a) => {
        const [spec, pkg] = a.path.split(" in ");
        const msg = `require("${spec.slice(5)}") in ${pkg} needs Node.js compatibility: add "nodejs_compat" to compatibility_flags in the wrangler config`;
        return { contents: `throw new Error(${JSON.stringify(msg)});\n`, loader: "js" };
      });
    },
  };
}

export async function buildApp(dir, { outfile = "dist/index.js", minify = false } = {}) {
  const w = readJSONC(path.join(dir, "wrangler.jsonc"));
  if (!w.main) throw new Error("wrangler.jsonc has no main");
  // Loaded here, not at the top: build-node.mjs imports readJSONC from this
  // file, and from an installed release esbuild (native, not vendored) is
  // only in the app's node_modules (build-node.mjs's loadEsbuild).
  const esbuild = await import("esbuild");
  const r = await esbuild.build({
    entryPoints: [path.join(dir, w.main)],
    outfile: path.resolve(dir, outfile),
    absWorkingDir: dir,
    bundle: true,
    format: "esm",
    platform: "neutral",
    target: "es2022",
    conditions: ["workerd", "worker", "browser"],
    mainFields: ["module", "main"],
    external: ["cloudflare:*", "node:*"],
    minify,
    // Module fallbacks for apps outside the tailworks repo (tw deploy sets this
    // to tailworks' own node_modules: @tailworks/tailnet, @tailworks/mcp, agents, …).
    nodePaths: (process.env.TW_NODE_PATH || "").split(":").filter(Boolean),
    legalComments: "none",
    logLevel: "warning",
    metafile: true,
    plugins: [requireBuiltinsJS(dir, (w.compatibility_flags || []).some((f) => f === "nodejs_compat" || f === "nodejs_compat_v2")), {
      name: "tw",
      setup(b) {
        // A dependency's import of cloudflare:email (under node_modules: the
        // Agents SDK imports it) gets the stub; the app's own import is
        // refused (internal/emailstub/refuse.go has the same rule and text).
        b.onResolve({ filter: /^cloudflare:email$/ }, (a) => a.importer.split(path.sep).includes("node_modules")
          ? { path: "email", namespace: "tw-stub" }
          : { errors: [{ text: "cloudflare:email is not supported on tailworks yet (imported by " + path.relative(realpathSync(dir), a.importer).split(path.sep).join("/") + ")" }] });
        b.onLoad({ filter: /.*/, namespace: "tw-stub" }, () => ({
          contents: "export class EmailMessage { constructor() { throw new Error('cloudflare:email is not supported on tailworks'); } }",
          loader: "js",
        }));
        b.onResolve({ filter: /^[a-z_][a-z0-9_/]*$/ }, (a) => {
          const base = a.path.split("/")[0];
          return builtins.has(base) || builtins.has(a.path) ? { path: "node:" + a.path, external: true } : undefined;
        });
      },
    }],
  });
  return { outfile: path.resolve(dir, outfile), bytes: Object.values(r.metafile.outputs)[0]?.bytes ?? 0 };
}

// CLI: node build.mjs <appdir> [outfile]
if (import.meta.url === `file://${process.argv[1]}`) {
  const dir = path.resolve(process.argv[2] ?? ".");
  buildApp(dir, { outfile: process.argv[3] ?? "dist/index.js" }).then(
    (r) => console.log(JSON.stringify(r)),
    (e) => { console.error(e.message); process.exit(1); },
  );
}
