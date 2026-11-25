Last update: 2026-11-24 10:00 PT. Built-in bundler handles CommonJS require() of Node builtins (#356).

# fix-356-cjs-require-builtins

## What changed

- `internal/esbundle/requirebuiltins.go`: an esbuild plugin, ahead of the
  one that marks builtins external. It catches `require()` (kind
  `require-call`) of a bare Node builtin or any `node:*` specifier:
  - with `nodejs_compat` / `nodejs_compat_v2` (`Options.NodeCompat`, set
    from the wrangler config's `compatibility_flags` in
    `cmd/tw/requirebuiltins.go`), it resolves to a CommonJS module
    `import * as m from "node:<name>"; module.exports = "default" in m ? m.default : m;`.
    Wrangler's hybrid plugin does `import libDefault from …; module.exports = libDefault`.
    The namespace form doesn't fail to link if a module has no default
    export. In workerd at 2026-06-11 every builtin has one, and it's the
    object Node's `require` returns (`process.getBuiltinModule(x) === default`).
  - without the flag, each `require()` resolves to a per-package stub that
    throws `require("fs") in <package> needs Node.js compatibility: add
    "nodejs_compat" to compatibility_flags in the wrangler config` when it
    runs. A static import isn't possible here: the node:* modules don't
    exist without the flag, so the Worker wouldn't link. Failing the build
    was the other option. It would break apps that build and run today
    with a guarded require that never runs (`typeof require` / `try`
    branches, as in tweetnacl). Imports of builtins without the flag pass
    through today with no build error, so the stub matches that.
  - `node:async_hooks` is always imported, because hostd adds `nodejs_als`
    to every tenant.
- `js/packages/create-tailworks/lib/build.mjs`: the same plugin
  (`requireBuiltinsJS`), with the flag read from `wrangler.jsonc`. Outputs
  stay byte-identical (`TestMatchesBuildMJS`, which now passes the
  template's flags to the Go side). The `agent` template bundle changes:
  a dependency's `require("path")` was a `__require` shim and is now an
  import of `node:path`.
- `e2e/compat`: the Vercel AI SDK examples (`ai-hono`, `ai-node-http`,
  `ai-express`) no longer add wrangler or deploy with `--wrangler`.
  COMPAT.md and upstream-results.json were regenerated from a fresh
  `make e2e-compat-dev` run (`make compat-upstream DEV=…`).

## Tests

- `go test ./internal/esbundle/`: ok. The new `TestRequireBuiltins` covers
  nodejs_compat, nodejs_compat_v2 and no flag, compares Go against
  build.mjs, and checks that `__require("` is gone. `TestImporterPackage`
  and `TestBuiltinName` are also new.
- `go test -run TestDevRequireBuiltins ./cmd/tw/`: ok. A dotenv-shaped
  CJS dependency (`require('fs')`, `require('path')`, `require('events')`)
  starts under `tw dev --once` in workerd and returns
  `function,a/b,function`. Without the flag, `tw dev` fails and the
  error names `fakedotenv` and `nodejs_compat`.
- `make e2e-wrangler`: PASS (TestDevWrangler 3/3, TestDevViteRedirect).
- `make e2e-compat-dev`: PASS, 17 PASS and 3 XFAIL (the expected #355
  and playground ones). ai-hono, ai-node-http and ai-express pass with
  the built-in bundler.
- `make lint`: ok.
- `make test` (mbp runner, `mbp-test.sh test`): ok, exit 0.

## Gaps

- `make e2e-compat` (deployed, real tailnet) not run. The "deployed"
  column of the three AI examples in COMPAT.md is still the earlier run
  that used `--wrangler`.
- Without nodejs_compat, an unguarded `require()` of a builtin fails at
  start-up, not at build. That is deliberate (see above), and the error
  says what to do.
