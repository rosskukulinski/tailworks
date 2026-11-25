# ADR-051: Cloudflare Pages projects are compiled by `tw` onto Workers with static assets

**Date:** 2026-11-23
**Status:** Accepted (2026-11-23; issue #285, built on branch
`pages-functions-issue-285`). Drafted as `ADR-pages-functions`.

**Context.**

- `tw` didn't recognise a Pages project. A Pages site deployed as
  assets-only served its files (and its `_headers`), but every
  `functions/` route answered 404. The workaround was to port each
  `functions/**` file by hand into a Worker `main` with
  `run_worker_first`.
- ADR-050 already serves both halves of a Pages site: a Worker, and files
  served by a router in front of it, with `env.ASSETS`, `run_worker_first`
  patterns, `not_found_handling`, `_redirects` and `_headers`.
- On Cloudflare, Pages compiles `functions/` into one Worker that sits in
  front of the static files (wrangler's filepath routing plus its
  `pages-template-worker` runtime), and an advanced-mode `_worker.js` is
  that Worker directly. `_routes.json` says which paths invoke it.
- Pages projects often keep bindings in the Cloudflare dashboard, not in
  git.

**Decision.**

1. **Detection.** A project is a Pages project when its wrangler config
   has `pages_build_output_dir`, or when it has no `main` and has
   `functions/` or `<assets.directory>/_worker.js`. A config with `main`
   is a Worker and its `functions/` directory is left alone.
   `pages_build_output_dir` together with `main` or `assets` is an error.
   `functions/` with no output directory, or with no wrangler config at
   all, fails with a fix that names `pages_build_output_dir`.
2. **Translation, in `tw` only.** `tw deploy` and `tw dev` rewrite the
   resolved config to the Workers-with-assets config it amounts to:
   - `pages_build_output_dir` becomes `assets.directory`;
   - `assets.binding` is `ASSETS` (the name Pages gives Functions) unless
     the config sets one;
   - `not_found_handling` follows Pages: `404-page` when the output
     directory has a `404.html` anywhere, `single-page-application` when it
     has none (only for `pages_build_output_dir`; an `assets` block keeps
     its own setting);
   - `_worker.js` and `_routes.json` are never uploaded as files.
   The bundle is an ordinary ADR-050 assets bundle. The host needs nothing
   new: it runs the same way on the process and microVM tiers, and mgmt
   places it on any host with the `assets` feature.
3. **Our own compiler, not wrangler's.** `internal/pages` walks
   `functions/` and generates one entry module, which the built-in
   esbuild bundles like any `main`. A deploy stays offline and needs no
   wrangler, as every other `tw deploy` does. The generated module follows
   wrangler's semantics, which the tests pin:
   - routes: `index` is its directory, `[id]` one segment
     (`params.id`), `[[path]]` zero or more (`params.path`, an array),
     nested directories (parameters too); matching is case-insensitive
     with an optional trailing slash, as path-to-regexp does;
   - order: more segments first; literal, then parameter, then catch-all;
     a route with a method before one without;
   - handlers: `onRequest` and `onRequestGet|Post|Put|Patch|Delete|Head|Options`,
     each a function or an array of functions;
   - `_middleware` at any level runs for its directory and below, least
     specific first, before the first route that matches exactly;
   - the EventContext: `request`, `env`, `params`, `data` (shared along the
     chain), `next(input?, init?)`, `waitUntil`, `passThroughOnException`,
     `functionPath`.
   A file that never mentions `onRequest` is a helper, not a route.
   `--wrangler` and `--from-wrangler-out` are refused for a Pages project
   with code (wrangler's Pages compiler is a different command, and its
   output would need a second reader).
4. **Routing onto assets.** The Worker's routes become
   `run_worker_first` patterns: wrangler's generated `_routes.json`
   include list (everything from the first parameter on is `*`; a
   middleware covers `dir` and `dir/*`; covered patterns dropped; more
   than 100 become `/*`). A project's own `_routes.json` replaces them
   (include, then `!`exclude). `next()` with nothing left, a method with no
   handler, and a path no route matches fall through to `env.ASSETS.fetch()`.
   Because ADR-050's front only routes `GET` and `HEAD`, the generated
   Worker also checks `_routes.json` itself, so an excluded path serves
   assets for every method, as on Cloudflare.
5. **Advanced mode.** `<output dir>/_worker.js` (a file, or a directory
   with `index.js`) is `main`, and `run_worker_first` is `true` (or
   `_routes.json`'s patterns, with the same every-method check in a small
   wrapper; the module's other exports pass through). `functions/` is then
   ignored with a warning, as on Cloudflare.
6. **Undeclared bindings.** At load, `tw` scans `functions/` for `env.X`,
   `env["X"]` and `{ X } = env` reads (not `process.env` or
   `import.meta.env`) and prints one warning naming each `env.X` that
   nothing declares (the names `tw types` would emit, plus binding sections
   tw already warns about), with where it is read, and says to declare
   the binding or `tw secret put` it and list it in `secrets.required`.
   It's a text scan: a hint, not a check. Advanced-mode `_worker.js` files
   are usually bundler output and are not scanned.
7. **`tw dev`** reloads on an edit under `functions/` (its change detector
   already walks the app directory) and in the output directory (ADR-050).
8. **`CF-Connecting-IP` is unchanged.** On a public route it is the client
   address Funnel reports (ADR-029). From a tailnet caller it is absent:
   netd strips client copies and sets none. A Function that rate-limits by
   IP should key on `getRequester(req)` (a login, tags, or the public IP)
   instead; the docs say so.
9. **Scope.** This exists to move existing Pages sites. New projects
   should use a Worker with `assets` (Cloudflare's own advice), which is
   the same thing without the translation.

**Consequences.**

- A Pages site with `functions/` deploys with `tw deploy`, unchanged,
  once it has a wrangler config with `pages_build_output_dir` and
  declares the bindings its dashboard used to hold.
- Static files still never reach the tenant isolate unless a function
  route or `_routes.json` covers them.
- tailworks now tracks Cloudflare's Pages routing rules itself. If
  Cloudflare changes them, `internal/pages` has to follow.

**Not in this version.** Pages-only platform features (branch preview
aliases, dashboard config and bindings, the Pages build system, the
`CF_PAGES*` build variables), `wrangler pages` CLI parity, Pages Plugins
(`context.pluginArgs`), `--wrangler` for Pages projects, and the binding
scan for `_worker.js`.

**Open questions.**

- Should a tailnet caller get `CF-Connecting-IP` (its tailnet address)?
  Not decided here; it would change netd for every workload, not only
  Pages ones.
