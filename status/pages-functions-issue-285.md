Last update: 2026-11-23 PT. Cloudflare Pages projects (`functions/`, `_worker.js`, `_routes.json`) deploy and run under `tw dev`; issue #285.

# Cloudflare Pages projects, issue #285

Design: `docs/design/adr/051-pages-functions.md` (ADR-051, numbered in the
merging PR; drafted as `ADR-pages-functions`). Builds on ADR-050 (static assets).

## What's built

- **Compiler** (`internal/pages`): walks `functions/` into wrangler-style
  routes (`index`, `[param]`, `[[catchall]]`, nested, `_middleware`; files
  that never mention `onRequest` are helpers), the generated
  `run_worker_first` patterns (wrangler's `_routes.json` include list),
  `_routes.json` parsing (version 1, ≤ 100 rules), and the generated entry
  module: each route imported by absolute path plus an embedded runtime
  (`runtime.js`, after wrangler's pages-template-worker: route order,
  middleware first, per-method handlers, handler arrays, the EventContext,
  `next()` / `next(url)`, `passThroughOnException`, fall-through to
  `env.ASSETS`, and a `_routes.json` check for every method). Advanced-mode
  wrapper for `_worker.js` behind `_routes.json`. `EnvNames` scans source
  for `env.X` reads.
- **CLI** (`cmd/tw/pages.go`): detection (`pages_build_output_dir`, or no
  `main` plus `functions/` or `<assets.directory>/_worker.js`); rewrites the
  resolved config to `assets` (`directory`, `binding: ASSETS`,
  `not_found_handling` 404-page / SPA by whether a `404.html` exists,
  `run_worker_first` from the routes, `_routes.json`, or `true` for
  `_worker.js`); `_worker.js` and `_routes.json` aren't uploaded; the
  built-in esbuild bundles the generated entry (`internal/esbundle` now
  takes an absolute `Main`); `--wrangler` / `--from-wrangler-out` refused
  for Pages with code; one warning for undeclared `env.X` in `functions/`
  (declared = `tw types` names + binding sections); clear errors for
  `main`/`assets` + `pages_build_output_dir`, a missing output dir, and
  `functions/` with no wrangler config. `pages_build_output_dir` no longer
  warns as an unknown key.
- **Host:** nothing new. The bundle is an ordinary ADR-050 assets bundle,
  so it needs the existing `assets` feature and runs the same on both tiers.
- **Docs:** `docs/wrangler.md#pages`, `docs/COMPAT.md` (`pages` row and a
  wrangler-config row), README compat row and a pointer, `changes/pages-functions-issue-285.md`.

## Tests (2026-11-23, after merging origin/master at e4d4422e)

- `make lint`: ok (re-run after the merge).
- CI first run: `docs:TestADRIndex` failed (the ADR wasn't numbered; CI enforces it). Numbered ADR-051; `CI=true go test ./docs/` passes.
- `make test`: ok (exit 0, -race Go and the JS suites; re-run after the merge).
- New: `internal/pages:TestDiscover`, `TestDiscoverErrors`, `TestPatterns`,
  `TestParseRoutesJSON`, `TestEnvNames`, `TestFunctionsEntry`;
  `hostd/gen:TestPagesFunctionsRouting`, `TestPagesFunctionsMiddleware`,
  `TestPagesFunctionsAssets`, `TestPagesRoutesJSON`, `TestPagesAdvancedMode`
  (real workerd behind the ADR-050 router; mutation-checked: dropping the
  middleware reverse or `passThroughOnException` fails them);
  `cmd/tw:TestPagesFunctionsBuild`, `TestPagesSPADefault`,
  `TestPagesAdvancedBuild`, `TestPagesDetection`, `TestPagesBuildErrors`,
  `TestPagesEnvWarning`.
- Manual: a Pages arcade-style site (D1 `env.DB`, `_middleware` chain,
  `[id]` DELETE, `[[path]]` with `next()`, 404.html) under real `tw dev`:
  every route, method, middleware header, asset and 404 answered as on
  Cloudflare; adding `functions/hello.js` reloaded in 54 ms; the
  undeclared `env.ADMIN_KEY` warning printed. `tw types` emits
  `ASSETS: Fetcher`.
- Not run: microVM tier (`e2e-vm` / `e2e-vz`). Nothing tier-specific
  changed (the bundle is a plain assets bundle, which ADR-050 verified on
  the microVM tier); `tw dev` covers the process tier.

## Gaps / next

- `CF-Connecting-IP` for tailnet callers (absent today) is an open
  question in the ADR.
- Pages Plugins (`pluginArgs`), `CF_PAGES*` variables, binding scan of
  `_worker.js`: not done.
