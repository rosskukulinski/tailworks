# ADR-050: wrangler `assets` are served by a generated router Worker in front of the app, from files in the bundle

**Date:** 2026-11-23
**Status:** Accepted (2026-11-23; issue #278, built on branch
`static-assets-278`). Drafted as `ADR-static-assets`.

**Context.**

- `tw deploy` and `tw dev` warned `assets: not supported` and ignored the
  key. An app could inline files into the Worker or import them as Text or
  Data modules (`rules`). That works for one small page, not for a built frontend: every file is a module in every
  isolate, with no `Content-Type`, `ETag` or HTML path rules.
- Cloudflare serves `assets` with an asset Worker in front of the app:
  files by path, `html_handling`, `not_found_handling`, `run_worker_first`,
  and `env.ASSETS.fetch()`. Ported projects (a Vite or React client next to
  an API Worker) expect those semantics.
- A deploy is already a multi-file bundle (64 MiB unpacked,
  `hostd/bundle.MaxBundle`); hostd already gives workerd `disk` services
  (DO storage); the microVM tier already copies the whole instance
  directory into the guest.
- mgmt places a deployment only on hosts that advertise every feature it
  needs (`internal/features`, ADR-041 §7).

**Decision.**

1. **The bundle carries the files under `assets/`.** `tw deploy` and
   `tw dev` read `assets.directory` (relative to the config file) and add
   every file as `assets/<path>`, outside the module graph. They skip
   `.assetsignore` patterns (a gitignore subset), `.git`, `node_modules`
   and `.DS_Store`. The resolved `wrangler.jsonc` keeps the `assets` settings.
   When `wrangler.jsonc` has `assets`, `hostd/bundle` treats `assets/` as
   files, never modules; without it, `assets/` is an ordinary directory,
   as before. Assets are read by `tw` itself, so they work the same with
   the built-in esbuild, `--wrangler` and `--from-wrangler-out`.
2. **hostd writes the files to the instance directory** (`assets/`) with a
   manifest (URL path → ETag, a truncated SHA-256 of the contents), and
   gives workerd a **read-only `disk` service** over them. The microVM
   guest gets them with the rest of the instance directory; its disk
   path is relative to the guest's app dir, where workerd runs.
3. **A generated router Worker** (`hostd/gen/js/assets.js`, service
   `assets`) serves them, following Cloudflare's asset Worker:
   - a `GET` or `HEAD` for a file gets it, with `Content-Type` from the
     extension, the manifest's strong `ETag`, `Cache-Control: public,
     max-age=0, must-revalidate`, and `304` for a matching
     `If-None-Match`;
   - `html_handling`: `auto-trailing-slash` (default),
     `force-trailing-slash`, `drop-trailing-slash`, `none`, with 307
     redirects for the other spellings of a page;
   - `not_found_handling` for `env.ASSETS.fetch()`: `404-page` (nearest
     `404.html`, status 404), `single-page-application` (`index.html`),
     `none` (empty 404);
   - it has no network: its `globalOutbound` is the deny service, and its
     only binding is the disk service.
4. **The router sits in front of the app in the tenant service.** The
   tenant main module's default export is `withAssets(<wrapped app>)`
   (`hostd/gen/js/assets-front.js`). A `GET`/`HEAD` that reached the
   workload (`cf.tw.kind == "workload"`, tailnet or public) asks the
   router first, through a binding with `props.router`; a file or redirect
   is the answer, a miss runs the app as before. Cron runs, queue
   deliveries, object routes (`/_tw/o/…` is the platform's) and non-GET
   methods never go through it. In `single-page-application` mode a
   browser navigation (`Sec-Fetch-Mode: navigate`) that misses gets
   `index.html` (Cloudflare's `assets_navigation_prefers_asset_serving`);
   other misses reach the Worker.
5. **`run_worker_first`:** `true` sends every request to the Worker (the
   front is skipped entirely); a list of patterns (`*` matches any
   characters, `/` included; `!` excludes) sends matching paths to the
   Worker. That is the place for a per-identity check: the Worker reads
   `getCaller(req)` and then calls `env.ASSETS.fetch(req)`.
6. **`env.<assets.binding>`** is a service binding to the router (no
   props): a file, or `not_found_handling`. It is in the Worker's env and
   in plain-mode objects; facet-mode objects (their own Dynamic Worker)
   don't get it in this version.
7. **Who can read assets:** anyone who can reach the workload, exactly as
   with the Worker: over the tailnet under the workload's grants, or over
   a `public` path (ADR-029). Files that need an identity check go under a
   `run_worker_first` path. This is Cloudflare's default (assets are
   public unless the Worker runs first) and keeps a file request off the
   tenant's isolate when no check is wanted.
8. **Limits:** assets count against the 64 MiB unpacked bundle, and the
   deploy request carries the bundle base64 in 64 MiB of JSON, so about
   48 MiB in practice. `tw` checks both before it uploads. A separate
   upload path for large sites is later work.
9. **Rollout:** a new host feature, `assets`. A bundle with `assets`
   requires it, so mgmt never places one on an older hostd, which would
   load the `.js` and `.html` files under `assets/` as modules (or not
   serve them).
10. **`tw dev`** deploys through the same hostd, and its change detector
    also watches `assets.directory` (even when it is `dist/`, which it
    otherwise skips), so an asset edit reloads.
11. **`tw types`** types `env.<binding>` as `Fetcher`.
12. **`_redirects` and `_headers`** (the root files of `directory`) are
    parsed once, in `hostd/bundle/assetrules.go`: by `tw` before the
    upload (a bad line fails with its line number) and by hostd at deploy,
    which hands the compiled rules (patterns as JS regexps) to the router.
    Neither file is served. Cloudflare's syntax and limits: `*` splat and
    `:name` segment placeholders; statuses 301, 302 (default), 303, 307,
    308 and 200 (a rewrite to a path on the site); 2,000 static and 100
    dynamic redirects; 100 header rules. Redirects run after
    `run_worker_first` and before file lookup, first match wins, for the
    router and `env.ASSETS` alike. Headers apply to file responses (304s
    and 404 pages too), not to redirects or Worker responses; a rule
    replaces the router's default header, matching rules join with `, `,
    and `! Name` removes. Domain-level rules are refused.
13. **Assets-only projects:** `assets` with no `main`. `tw` bundles no
    code; `hostd/bundle` supplies a stand-in Worker
    (`bundle.AssetsOnlyMain`), and the router answers every `GET` and
    `HEAD`, with `not_found_handling` for any miss (a single-page app gets
    `index.html` whatever the request mode) and 405 for other methods.
    `run_worker_first` is an error there. Only a host with the `assets`
    feature accepts such a bundle (an older one fails it for no `main`).

**Consequences.**

- A Vite/React client or a docs site deploys next to its API with
  Cloudflare's config; the `static-site` template shows it with no build
  step, so it deploys offline from a release.
- Serving a file costs a service-binding hop from the tenant isolate to
  the router and a disk read; the Worker's own code doesn't run.
- Asset requests enter through the tenant service's `fetch` like any
  other request to the workload, so ingress, grants and public-path rules
  apply to them unchanged.
- Old bundles and apps without `assets` are unchanged: no extra services,
  modules or bindings are generated.

**Not in this version.** `env.ASSETS` in facet-mode objects; a separate
upload path; domain-level `_redirects` / `_headers` rules; content-hash
deduplication across deploys; precompressed variants.

**Open questions (from #278).**

- Should templates ship prebuilt assets? This change answers it for a
  plain site (`static-site` has no build step). A template with a built
  client would have to commit its build or build at deploy time; not
  decided here.
