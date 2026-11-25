---
kind: feature
upgrade: []
---
Cloudflare Pages projects deploy with `tw deploy` and run under `tw dev`.
A wrangler config with `pages_build_output_dir` (or no `main` plus a
`functions/` directory or a `_worker.js`) is a Pages project: the output
directory is served as static assets, and `tw` compiles `functions/` into
the Worker itself, with no wrangler needed. File-based routing
(`[param]`, `[[catchall]]`, nested directories), `onRequest` and
per-method handlers, `_middleware` chains, the full EventContext
(`params`, `data`, `next()`, `waitUntil`, `passThroughOnException`,
`functionPath`), `_routes.json` and advanced-mode `_worker.js` work as on
Cloudflare. `tw dev` reloads on an edit under `functions/`. `tw` warns
when `functions/` reads an `env.X` that the config doesn't declare (Pages
dashboard bindings aren't in git). See docs/wrangler.md#pages.

The bundle is an ordinary static-assets bundle, so hosts need the
`assets` feature (an earlier release) and nothing new.
