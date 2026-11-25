# Using wrangler with tailworks

An existing Workers project runs on tailworks without changes. `tw deploy` and
`tw dev` read wrangler's own config file, bundle the project the way
wrangler does (with tailworks' built-in esbuild or with wrangler itself), and say
which settings they ignore.

```sh
cd my-worker            # wrangler.toml, src/index.ts, no tw files
tw dev                 # local run: tw-hostd + workerd, no tailnet
tw deploy              # to your tw stack
tw deploy --env staging
```

What tailworks does not use is `wrangler deploy` itself, or the other wrangler
commands that call Cloudflare's API. See [below](#wrangler-commands-that-call-cloudflare) for why.

## What's different from Cloudflare, in short

- **No Cloudflare account, no edge.** Your code runs on your own machines.
  A workload's address is a MagicDNS name on your tailnet, not
  `*.workers.dev` or a route on your zone.
- **Private by default.** Only devices on your tailnet can call a workload,
  and only if your tailnet policy lets them. Public paths are opt-in, over
  Funnel ([public-ingress.md](public-ingress.md)).
- **Callers are tailnet identities.** `getCaller` gives the caller's login
  or tags from WireGuard. There are no API tokens or Access JWTs to check.
- **Outbound `fetch()` reaches tailnet peers and the internet.** Tailnet
  calls leave from the workload's (or object's) own node. Internet calls
  work as on Cloudflare, but leave from the host's network, so the API
  sees the host's address. Private and metadata addresses are refused.
  `tailnet.egress.internet: false` in tailworks.jsonc turns internet
  egress off, and a list limits it
  ([recipes.md](recipes.md#call-an-internet-api)).
- **No geolocation in `request.cf`.** `country`, `colo`, `latitude` and
  the other geo fields are `undefined`; feature-detect them
  ([below](#requestcf)).
- **Each Durable Object is a tailnet node** with its own name, tags and
  grants. Its storage lives on one host's disk, with no replication.
- **Same CLI shapes, different binary.** `tw dev`, `tw deploy`,
  `tw secret`, `tw kv`, `tw d1`, `tw queues`, `tw r2` and `tw logs`
  stand in for their wrangler commands. Secrets apply on the next deploy.
- **Service bindings go over the tailnet.** `env.AUTH.fetch()` and RPC
  to another workload's `WorkerEntrypoint` work, as the calling node, so
  they need a grant like any tailnet call ([below](#service-bindings)).
- **Not there:** Workers AI, Vectorize, Hyperdrive, Browser Rendering,
  Email, TCP `connect()`. Static assets work, with
  `_redirects` and `_headers` ([below](#assets)), and so do Cloudflare
  Pages projects with `functions/` ([below](#pages)). The full list,
  with tests: [COMPAT.md](COMPAT.md).

## Config files

tailworks reads the same files as wrangler 4, in the same order. The first one
found in the app directory wins:

1. `wrangler.json`
2. `wrangler.jsonc`
3. `wrangler.toml`

If there are others, `tw deploy` prints one line for each, saying it's
ignored. Unlike wrangler, tailworks only looks in the app directory, not its
parents, because `tailworks.jsonc` sits there too.

Whatever the format, the bundle carries the resolved config as JSON
(`wrangler.jsonc`), so the host never sees TOML or environments.

### <a id="vite"></a>Projects built with the Cloudflare Vite plugin

`vite build` with `@cloudflare/vite-plugin` (React Router, the React and
Workflows starters, Agents SDK examples) writes the Worker it built, with a
wrangler config of its own, to the build output (`dist/<worker>/` or
`build/server/`). It points `.wrangler/deploy/config.json` at that config.
Like `wrangler deploy`, `tw deploy`, `tw dev`, `tw policy` and `tw types`
follow the pointer when the project has one, and print one line saying so:

```
› using build/server/wrangler.json, the config `vite build` wrote (.wrangler/deploy/config.json), as wrangler deploy does
```

So `npm run build && tw deploy` deploys what the build produced, with the
assets directory the build chose. `tailworks.jsonc` still comes from the
project directory. Paths in the generated config (`main`, `assets`,
`migrations_dir`, container Dockerfiles) are relative to that config, as in
wrangler. The generated config already holds one environment, so `--env`
is refused: build with `CLOUDFLARE_ENV=<name> npm run build` instead.
Auxiliary Workers in the pointer are not deployed, with a line each. Delete
`.wrangler/deploy/config.json` to go back to the project's own config, and
rebuild after changing your sources: tailworks doesn't check that the build
is up to date.

The generated config has `"no_bundle": true` and an ES-module rule for
`**/*.js` and `**/*.mjs`. The built-in path then packs the pre-built modules
as they are, without running esbuild ([below](#no-bundle)).

## Environments (`--env`)

`tw deploy --env staging` and `tw dev --env staging` use `[env.staging]`
the way `wrangler --env staging` does:

- These keys are **inherited** from the top level unless the environment
  sets them: `main`, `compatibility_date`, `compatibility_flags`,
  `migrations`, `triggers`, `placement`, the build settings, and the rest.
- These keys are **not inherited**: `vars`, `define`, `durable_objects`,
  `containers` and every other binding. An environment without its own copy
  has none. Tailworks prints a line for each one, as wrangler does:

  ```
  › wrangler.toml: env.staging: define is set at the top level but not in env.staging; environments don't inherit it (as in wrangler), so env.staging has none
  ```

- The **workload name** is `<name>-<env>` (for example `shop-staging`),
  unless the environment sets its own `name`. `<name>` is `tailworks.jsonc`
  `name` if there is one, then wrangler's `name`, then the directory name.
  `--name` overrides all of these.

So `staging` and production are two workloads with two tailnet nodes. If
`tailworks.jsonc` exists, it is shared by every environment. It has no `env`
section, so tags it lists are the same in both, and so are their grants. An
omitted `tailnet.tags` follows the workload name (`tag:tw-wl-<workload>`),
so then each environment gets its own tag (`tag:tw-wl-shop-staging`), and
`tw policy` prints its `tagOwners` line.

Secrets belong to a workload, so give an environment's secrets its name:
`tw secret put API_KEY --workload shop-staging`. Run in the app
directory without `--workload`, `tw secret` uses the top-level name.

An unknown environment is an error that lists the ones that exist.

## Without a tailworks.jsonc

A project that has only a wrangler config gets these defaults:

```jsonc
{
  "name": "<workload>",
  "tailnet": {
    "tags": ["tag:tw-wl-<workload>"],
    // every durable_objects class (as for any class a tailworks.jsonc leaves out):
    "objects": { "<Class>": { "node": false } }
  }
}
```

The workload node is tagged `tag:tw-wl-<workload>`. As with any new tag,
`tw policy` prints the `tagOwners` line to add (and the first `tw deploy`
stops at preflight until it's there).

Durable Object classes are **internal**: you reach them through their
binding, as on Cloudflare, and they have no tailnet node of their own (see
[egress-modes.md](egress-modes.md)).

To give the workload `connects`, a Service or public routes, or to give a
class its own node and tags, add a `tailworks.jsonc` ([tailworks-jsonc.md](tailworks-jsonc.md)).
`tw deploy` says when it is using the defaults.

## Bundling

There are three ways to bundle. All three produce the same bundle shape:
the entry module plus additional modules named `<sha1>-<file>`, exactly as
wrangler names them.

| | built-in esbuild (default) | `--wrangler` | `--from-wrangler-out <dir>` |
|---|---|---|---|
| what runs | esbuild 0.28.2, linked into `tw` | `wrangler deploy --dry-run --outdir <tmp> --config <file> --env=<env>` in the app directory. Nothing is sent to Cloudflare | nothing: tailworks packs a directory you built with `wrangler deploy --dry-run --outdir <dir>` |
| needs Node.js | no | yes, and wrangler (`node_modules/.bin/wrangler`, then `PATH`; `$TW_WRANGLER` overrides) | no (at deploy time) |
| use it for | most projects, including a [custom build](#custom-builds) | to get exactly wrangler's output, or the `rules` / `no_bundle` settings below | CI that already runs wrangler, or a pinned wrangler version |

For `--from-wrangler-out`, tailworks finds the entry by `main`'s name
(`src/index.ts` becomes `index.js`). It leaves out `README.md` and source
maps. Each other module gets its type from your `rules` and wrangler's
default rules, matched on its file name, and otherwise from its extension.
Tailworks doesn't check that the directory is up to date with your sources.

<a id="custom-builds"></a>A config with a custom build
(`"build": {"command": "…"}`) runs that command before the built-in esbuild
bundles, as wrangler does: in a shell (`sh -c`; `cmd /c` on Windows), in
`build.cwd` (default: the config's directory), with every
`node_modules/.bin` from there up first on `PATH`, as under `npm run`.
`tw deploy` and `tw policy` print what ran and stream its output on stderr:

```
› build      esbuild src/client/index.tsx --bundle --splitting --format=esm --platform=browser --outdir=public/dist
```

If it exits non-zero, nothing is deployed (`build_command_failed`).
`tw dev` runs it once at start, then again only when a file under
`build.watch_dir` (default `./src`, as wrangler) changes; what the build
writes (often under `assets.directory`) doesn't count as an edit, so it
can't trigger another reload. It is your project's own command, run on
your machine, as `npm run build` would be. `--wrangler` leaves it to
wrangler, and a [Pages project](#pages), a [pre-built](#no-bundle)
`no_bundle` Worker and a `runtime: "node"` app don't run it.

<a id="no-bundle"></a>A config with `"no_bundle": true` and `rules` of type
ESModule whose globs are only `**/*.js` and `**/*.mjs` (what the
[Vite plugin](#vite) writes) describes modules that are already built. The
built-in path packs them as they are instead of bundling: `main`, plus
every file under `base_dir` (default: `main`'s directory) that a rule types,
at its path there, unless `find_additional_modules` is `false`. Other files
(the build's `wrangler.json`, `.vite/manifest.json`, source maps) are left
out. The build line names the bundler `prebuilt`. Other `no_bundle` configs
still need `--wrangler`.

### What each bundler honours

Measured with `e2e/wrangler/testdata/toml-app` and wrangler 4.60.0
(`make e2e-wrangler`, `TestBuildWranglerTOMLApp`):

| wrangler setting | built-in esbuild | wrangler | notes |
|---|---|---|---|
| `main` (TS or JS) | ✅ | ✅ | |
| `define` | ✅ | ✅ | |
| `alias` | ✅ | ✅ | |
| default `rules`: `.txt` / `.html` / `.sql` as Text, `.bin` as Data, `.wasm` / `.wasm?module` as CompiledWasm | ✅ | ✅ | same `<sha1>-<file>` module names |
| custom `rules` of type Text, Data or CompiledWasm (with `fallthrough`) | ✅ | ✅ | e.g. `**/*.md` as Text. The host types each module from `tw-bundle.json` `modules` |
| `no_bundle` with ESModule `rules` for only `**/*.js` / `**/*.mjs` (pre-built, e.g. Vite plugin output), with `find_additional_modules`, `base_dir` | ✅ packed as they are ([pre-built](#no-bundle)) | ✅ | |
| other `rules` of type ESModule / CommonJS, `no_bundle`, `find_additional_modules`, `base_dir`, `preserve_file_names` | ❌ warned | ✅ ES modules | tailworks ships ES modules only; a `.cjs` module in wrangler's output is an error |
| `build` (custom build command) | ✅ runs `build.command` first ([custom builds](#custom-builds)) | ✅ | not for a Pages project or a pre-built `no_bundle` Worker (warned) |
| `minify`, `keep_names`, `jsx_factory`, `jsx_fragment`, `tsconfig` | ✅ mapped to esbuild options | ✅ | mapped, but not measured end to end |
| `nodejs_compat` | ✅ bare built-ins become `node:*`; workerd provides them | ✅ plus wrangler's unenv polyfills | toml-app's entry: 1.0 KB built-in, 21 KB wrangler. Both pass the same checks (`node:buffer`) |
| `node_compat` (legacy) | ❌ warned | ❌ wrangler 4 rejects it | use `compatibility_flags = ["nodejs_compat"]` |
| `upload_source_maps` | ✅ on unless `false` | ❌ warned | the built-in esbuild ships `dist/index.js.map` (no sources); hosts map exception stacks to `src/…` with it ([dev-debugging.md](dev-debugging.md)) |
| `assets` (static assets) | ✅ | ✅ | read by `tw` itself, whichever bundler builds the Worker ([Static assets](#assets)) |
| `site` (Workers Sites) | ❌ warned | ❌ | deprecated by Cloudflare; use `assets` |

`create-tailworks`'s `build.mjs` (the npm bundler) still reads only
`wrangler.jsonc` and has none of these settings. `tw deploy` does not
use it.

## <a id="python"></a>Python Workers

A config whose `main` is a `.py` module, with `"python_workers"` in
`compatibility_flags`, is a Python Worker. It isn't bundled: `tw deploy`
ships the files under `main`'s directory and the vendored `python_modules/`,
as pywrangler does, and hostd runs them in Pyodide. `--wrangler` and
`--from-wrangler-out` don't apply (they bundle JavaScript). Vendor packages
with `uv run pywrangler sync` before `tw deploy`. Everything else, and what
isn't supported yet: [python.md](python.md).

## Settings tailworks ignores

`tw deploy` and `tw dev` print one line per top-level key they don't use,
before anything else. Nothing is ignored silently:

```
› wrangler.toml: vectorize: no such binding on tw: env.CACHE will be undefined
› wrangler.toml: routes: ignored: there is no Cloudflare zone or workers.dev; the workload answers at its MagicDNS name (tailworks.jsonc "public" for the internet)
› wrangler.toml: workers_dev: ignored: there is no Cloudflare zone or workers.dev; …
› no tailworks.jsonc: using defaults (workload node tag:tw-wl-wtoml; Durable Object classes internal, no node of their own); add a tailworks.jsonc to change them
```

`tw deploy --json` returns the same lines in `warnings`.

| keys | what tailworks says |
|---|---|
| `routes`, `route`, `workers_dev`, `preview_urls`, `account_id`, `zone_id` | ignored: the workload is reached at its MagicDNS name. Public routes are `tailworks.jsonc` `public` ([public-ingress.md](public-ingress.md)) |
| `ai`, `browser`, `vectorize`, `hyperdrive`, `analytics_engine_datasets`, `send_email`, `dispatch_namespaces`, `mtls_certificates`, `images`, `pipelines`, `ratelimits`, `secrets_store_secrets`, `version_metadata`, `worker_loaders`, `vpc_services`, `media`, `tail_consumers`, `logfwdr`, `unsafe` | no such binding on tailworks; the warning names each `env.X` that will be undefined. A section tailworks supports as a [platform store](design/adr/044-platform-stores.md) (`kv_namespaces`, [Workers KV](kv.md); `r2_buckets`, [R2](r2.md); `d1_databases`, [D1](d1.md)) or as [service bindings](#service-bindings) (`services`) is not warned about |
| `site` | not supported (Workers Sites; use `assets`) |
| `dev` other than `dev.port` (`ip`, `local_protocol`, `host`, …) | ignored by tailworks: `tw dev` serves plain HTTP on 127.0.0.1 only. `dev.port` is `tw dev`'s port, as in `wrangler dev` |
| `observability`, `logpush`, `limits`, `usage_model`, `keep_vars`, `send_metrics`, … | ignored by tailworks |
| `python_modules` (its `exclude` globs) | ignored: `tw deploy` ships `python_modules/` whole, less `__pycache__` and `*.pyc` ([python.md](python.md#modules-and-files)) |
| a `durable_objects` binding with `script_name` | not supported (the deploy fails) |
| anything else | unknown key, ignored |

These keys are used without a warning: `name`, `main`, `compatibility_date`,
`compatibility_flags`, `vars`, `durable_objects`, `kv_namespaces` ([kv.md](kv.md)), `r2_buckets` ([r2.md](r2.md)), `migrations`, `triggers`
([cron-triggers.md](cron-triggers.md)), `containers`
([containers.md](containers.md)), `placement`, `assets` ([below](#assets)),
`pages_build_output_dir` ([below](#pages)), `secrets` (its `required` names, for `tw types`), and the build settings
above.

<a id="assets"></a>

## Static assets (`assets`)

A folder of files served next to the Worker, as on Cloudflare: a plain site,
or the build output of a Vite, React or docs project. The `static-site`
template is a working example (`tw init my-site --template static-site`).

```jsonc
"assets": {
  "directory": "./public",                 // required: the folder to serve
  "binding": "ASSETS",                     // optional: env.ASSETS.fetch(req) in the Worker
  "html_handling": "auto-trailing-slash",  // the default; or force-trailing-slash, drop-trailing-slash, none
  "not_found_handling": "404-page",        // or single-page-application, none (the default)
  "run_worker_first": ["/api/*"]           // or true: these paths run the Worker before any file
}
```

How a request is answered:

1. A path matching `run_worker_first` (`true` is every path; `*` matches
   any characters, `/` too; a `!` pattern excludes) goes to the Worker.
   It can answer, or check `getCaller(req)` and then serve a file with
   `env.ASSETS.fetch(req)`.
2. A `_redirects` rule that matches the path (first match wins) answers
   with its redirect, or, for status `200`, with its destination's file
   at the requested URL.
3. A `GET` or `HEAD` for a file is answered with the file: `Content-Type`
   from its extension, a strong `ETag`, `Cache-Control: public, max-age=0,
   must-revalidate`, and `304 Not Modified` for a matching `If-None-Match`.
   `html_handling` maps `/about` to `about.html`, `/docs/` to
   `docs/index.html`, and redirects (307) the other spellings, the way
   Cloudflare does.
4. Anything else goes to the Worker. In `single-page-application` mode a
   browser navigation (`Sec-Fetch-Mode: navigate`) that matches no file
   gets `index.html` instead.

`env.ASSETS.fetch(req)` applies `not_found_handling` to a path with no file:
`404-page` serves the nearest `404.html` (status 404),
`single-page-application` serves `index.html`, `none` answers an empty 404.

What gets uploaded: every file under `directory`, except the patterns in
its `.assetsignore` (gitignore-style: `*.map`, `private/`, `/top.txt`;
no `!` or `**`), `.git`, `node_modules` and `.DS_Store`.

### `_redirects` and `_headers`

Put them at the top of `directory`. They are read by the host, never
served as files, and `tw deploy` / `tw dev` reject a bad line (with its
line number) before uploading.

```
# _redirects: <from> <to> [status]
/old             /new                     # 302 by default
/moved           /new             301     # 301, 302, 303, 307 or 308
/blog/*          /posts/:splat    308     # * is a splat (any characters, / too)
/u/:id/profile   /people/:id              # :name is one path segment
/docs/*          https://docs.example.com/:splat
/app/*           /index.html      200     # a rewrite: that file, at this URL
```

```
# _headers: a path pattern, then indented headers
/*
  X-Frame-Options: DENY
/assets/*
  Cache-Control: public, max-age=31536000, immutable
  ! X-Frame-Options
```

- Rules match the URL path as sent (percent-encoded). `_redirects` keeps
  the request's query string unless the destination has its own. A `200`
  rewrite only names a path on the site; a destination with no file is a
  miss.
- `_headers` apply to file responses (including 304s and the 404 page),
  not to redirects or the Worker's own responses. A rule's header replaces
  tailworks' default (`Cache-Control`, `Content-Type`); the same header
  from several matching rules is joined with `, `; `! Name` removes it.
  `:splat` and `:name` fill in values too.
- Limits, as on Cloudflare: 2,000 static and 100 dynamic redirects (1,000
  characters a line); 100 header rules (2,000 characters a line). Only
  paths: domain-level rules (`https://host/…` as the pattern) are refused.

### Assets-only projects

A config with `assets` and no `main` is a site with no Worker:

```jsonc
{ "name": "docs", "assets": { "directory": "./dist", "not_found_handling": "single-page-application" } }
```

Every `GET` and `HEAD` is answered from the files, and a miss gets
`not_found_handling` (a single-page app gets `index.html` for any path,
navigation or not). Other methods get `405`. `run_worker_first` needs a
Worker, so it is an error here. `tw` bundles no code; the host supplies
the stand-in Worker.

`tw dev` serves the same way and reloads when a file in `directory`
changes, even when it is `dist/`. Build your client (for example
`npm run build`) before `tw deploy`, or set the wrangler config's
`build.command` and tailworks runs it first ([custom builds](#custom-builds)).

Differences from Cloudflare:

- The files ride in the deploy bundle, so they count against its 64 MiB
  (about 48 MiB in one deploy request, which carries the bundle as JSON).
  Larger sites need a separate upload path, which doesn't exist yet.
- Who can read: anyone who can reach the workload, over the tailnet or a
  [public](public-ingress.md) path. Put files that need an identity check
  under a `run_worker_first` path.
- `env.ASSETS` is in the Worker and in plain-mode Durable Objects
  (`"egress": "workload"`), not in facet-mode objects.
- The host must run a tailworks release with the `assets` feature; mgmt
  doesn't place an assets deployment on an older host.

## `request.cf`

On Cloudflare, `request.cf` carries the client's location and the edge
that served it. tailworks has neither, so **none of the geolocation fields
are set**: `country`, `colo`, `city`, `region`, `regionCode`, `continent`,
`postalCode`, `metroCode`, `latitude`, `longitude`, `timezone`,
`isEUCountry`, `asn` and `asOrganization` are all `undefined`. They are
absent, not set to `""` or `0`, so a check like `cf?.latitude !== undefined`
tells you whether you have a location.

- A tailnet caller connects from a `100.x` address, which has no IP
  geolocation.
- A public (Funnel) request has a real client IP
  (`getRequester(req).ip`, `CF-Connecting-IP`), but tailworks doesn't
  look it up in a GeoIP database.

What tailworks does put in `request.cf`:

| Key | What it is |
|---|---|
| `tailscale` | who sent the request: `caller` (the verified tailnet caller, with its `seal`) or, for a public request, `requester` (`{kind: "public", ip, ...}`). Read it with `getCaller(req)` / `getRequester(req)`, not directly |
| `objectId` | the tailworks object (node) the request arrived on |
| `traceId` | tailworks' audit trace ID for this hop |
| `tw` | platform routing (which workload or object, the create ticket). Internal; don't depend on its shape |

`request.cf` survives a service binding and a Durable Object stub's
`fetch()`, so an object sees the same keys as the Worker.

Ported code that reads a location has to feature-detect it and fall back:
ask the client (the browser's Geolocation API, a form field), or look the
public IP up yourself. For example, a Durable Object's `onConnect` that
needs a position should handle `ctx.request.cf?.latitude === undefined`
rather than drop the connection.

<a id="pages"></a>
## Cloudflare Pages projects (`functions/`, `_worker.js`)

An existing Pages site deploys as it is: a folder of built files plus
`functions/`, or an advanced-mode `_worker.js`. `tw` compiles the
Functions into the Worker itself (no wrangler needed), so Pages becomes a
Worker with [static assets](#assets). For a new project, use a Worker
with `assets` directly, as Cloudflare also recommends.

```jsonc
// wrangler.jsonc: Pages sites often have none; this is all it needs
{
  "name": "arcade",
  "compatibility_date": "2026-04-01",
  "pages_build_output_dir": "./public",   // the folder your build writes
  "d1_databases": [{ "binding": "DB", "database_name": "arcade", "database_id": "…" }]
}
```

```sh
npm run build    # if the site has a build step: tailworks doesn't run it
tw dev           # reloads on edits under functions/ and public/
tw deploy
```

A project is treated as Pages when the config has
`pages_build_output_dir`, or when it has no `main` and has `functions/` or
`<assets.directory>/_worker.js`. `tw deploy` prints
`Cloudflare Pages project: …` when it does.

What carries over, with Cloudflare's semantics:

- **Routing** from file names: `functions/index.js` is `/`,
  `api/scores.js` is `/api/scores`, `api/scores/[id].js` is
  `/api/scores/:id` (`params.id`), `files/[[path]].js` is `/files` and
  everything under it (`params.path`, an array). The more specific route
  wins (a literal beats `[id]`, which beats `[[path]]`).
- **Handlers:** `onRequest`, and `onRequestGet`, `onRequestPost`, `…Put`,
  `…Patch`, `…Delete`, `…Head`, `…Options`; each a function or an array.
- **Middleware:** `_middleware.js` at any level, for that directory and
  below, outermost first; `export const onRequest = [a, b]` chains.
- **The context:** `request`, `env`, `params`, `data`, `next()`,
  `next(url)`, `waitUntil`, `passThroughOnException`, `functionPath`.
- **Assets:** `env.ASSETS.fetch()`; `next()` with no handler left, and any
  path no function matches, is served from the files. `_redirects` and
  `_headers` work as [above](#_redirects-and-_headers) (headers apply to
  files, not to Function responses, as on Pages). A site with no
  `404.html` is served as a single-page app, as on Pages.
- **`_routes.json`** (in the output directory) limits which paths run the
  Functions; an excluded path serves files for every method. Without one,
  only the paths of your function routes reach the Worker; other files
  never run code.
- **Advanced mode:** `<output dir>/_worker.js`, a file or a directory with
  `index.js`, is the Worker for every request (or the `_routes.json`
  paths). `functions/` is then ignored, as on Cloudflare. Neither
  `_worker.js` nor `_routes.json` is served.

Differences from Cloudflare:

- **Bindings must be in the config.** Bindings and secrets set in the
  Pages dashboard aren't in git, so declare them in the wrangler config
  (`d1_databases`, `kv_namespaces`, `vars`, …) and set secrets with
  `tw secret put` (`.dev.vars` for `tw dev`). When `functions/` reads an
  `env.X` that nothing declares, `tw` warns, naming the file:
  `functions/ reads env.ADMIN_KEY (api/scores.js), but wrangler.jsonc doesn't declare it`.
  Listing a secret in `secrets.required` silences it.
- **`CF-Connecting-IP`** is the client address on a
  [public](public-ingress.md) route, and absent for a tailnet caller. Key a
  rate limit on `getRequester(req)` (a login, tags, or the public IP)
  rather than on the header.
- **Not supported:** branch preview aliases, dashboard config, the Pages
  build system (run your build first), the `CF_PAGES*` variables, Pages
  Plugins (`context.pluginArgs`), and `wrangler pages` commands.
  `--wrangler` doesn't build a Pages project; the built-in compiler does.

## Service bindings (`services`)

<a id="service-bindings"></a>
A `services` entry binds another workload **of the same tenant** (the
tailworks counterpart of a Cloudflare account), as on Cloudflare:

```jsonc
// wrangler.jsonc of the "shop" workload
"services": [
  { "binding": "AUTH", "service": "auth" },                         // auth's default export
  { "binding": "ADMIN", "service": "auth", "entrypoint": "Admin" }, // export class Admin extends WorkerEntrypoint
]
```

```js
const r = await env.AUTH.fetch(request);      // a fetch() to auth
const ok = await env.AUTH.check(token);       // RPC to auth's default WorkerEntrypoint
const n = await env.ADMIN.countUsers();       // RPC to the Admin entrypoint
```

- **It's a tailnet call.** tailworks resolves `service` to that
  workload's entry node and sends the call from the calling node: the
  Worker's workload node, or a facet-mode object's own node. The target
  can be on the same host (netd delivers it in process) or another host.
  As for any tailnet call, the caller's tags need a grant to the target's
  (`tw policy` prints it).
- **The target knows who called.** `getCaller(request)` in `fetch()`, and
  `getCaller(this.ctx)` in an RPC method, name the calling node. A caller
  on the Request or in headers never gets through.
- **Named entrypoints and RPC methods** answer only tailworks workloads
  and objects of the same tenant ([`binding_forbidden`](errors.md#binding_forbidden)
  otherwise). The target's default `fetch()` is an ordinary tailnet
  endpoint.
- **The grant is the authorization, not the binding.** On Cloudflare a
  Worker reaches another Worker's entrypoint only through a binding its
  own config declares. On tailworks the target checks who called (a node
  of the tenant that the tailnet policy lets in), not that the caller
  declared the binding. A plain `fetch()` can't ask for an entrypoint
  (tailworks removes the header that does), but code that opens a raw
  socket to the target can. In an entrypoint or RPC method that must not
  be open to every workload of the tenant, check `getCaller()`
  ([security model](security-model.md#known-gaps)).
- **RPC values are copied**, as JSON over the tailnet: everything
  structured clone handles (objects, arrays, `Date`, `Map`, `Set`,
  `RegExp`, `BigInt`, typed arrays, `ArrayBuffer`, errors, `undefined`,
  `NaN`). What Cloudflare passes by reference (functions, `RpcTarget`
  stubs, streams, `Request`, `Response`) is refused with
  [`rpc_not_forwardable`](errors.md#rpc_not_forwardable); send a body
  with `fetch()` instead. A thrown error keeps its name and message.
- **The target can be a Python Worker**: RPC reaches its `Default` or
  named `WorkerEntrypoint` class's methods ([python.md](python.md#rpc-from-other-workers)).
- **Errors:** a target that isn't deployed in the tenant, or an
  `entrypoint` it doesn't export, is
  [`service_not_found`](errors.md#service_not_found); a killed one is
  `killed`.
- Not supported: `environment` and `props` (refused at deploy), RPC
  promise pipelining, a request and response streaming at the same time
  (each streams on its own), and, in `tw dev`, a binding to another
  workload.
- **When the target moves**, calls follow it: the target is resolved
  again at most 5 s after its last lookup (sooner when its old node is
  gone), plus the time to reach a new node. While a takeover is waiting
  for the old host's lease to run out, calls fail or wait. Once the target
  is undeployed (or killed), calls stop reaching it within those 5 s. The
  caller the target sees is the calling copy's own node, across moves of
  either side. The data-plane chaos loop checks all of this under faults
  (`e2e/dataplane:TestRouting`, scenario `bindings`).

`tw types` types each binding as `Fetcher`.

## Types: `tw types`

`tw types` is tailworks' `wrangler types`. It writes `worker-configuration.d.ts`
in the app directory: a global `Env` interface built from the wrangler
config and `tailworks.jsonc`.

| source | in `Env` as |
|---|---|
| `vars` | `string`, `number`, `boolean` or an object type (widened, not the literal value) |
| `durable_objects` bindings | `DurableObjectNamespace<import("./src/index").Class>` (plain `DurableObjectNamespace` for a `script_name` binding) |
| `kv_namespaces`, `r2_buckets`, `d1_databases` | `KVNamespace`, `R2Bucket`, `D1Database` |
| `queues.producers` | `Queue` |
| `workflows` | `Workflow` |
| secret names: `.dev.vars`, `.dev.vars.example`, `secrets.required` | `string` |

Bindings tailworks doesn't support are left out, because on tailworks they are
undefined. `--env <name>` types that environment. A `"runtime": "node"` app
gets `NodeJS.ProcessEnv` instead: the variables tailworks sets, `node.env` and the
secret names.

Every `tw init` template ships the file along with a `tsconfig.json`
(`@cloudflare/workers-types` plus `@tailworks/tailnet`, which declares
`ctx.tailnet`), so `npm install && npx tsc --noEmit` passes. The templates
use that global `Env` and don't declare their own: in an ES module a local
`interface Env` would hide the generated one. `tw dev` keeps the file
current: on start and on each reload it rewrites a
`worker-configuration.d.ts` that `tw types` wrote when the config has
changed. It never creates the file, and it leaves alone a hand-written
one or one typed for an `--env`. It does nothing under `tw dev --env`, `--wrangler`,
`--from-wrangler-out` or `--once`. Otherwise, run `tw types` again after
you change the config. In CI, run
`tw types --check`: it writes nothing and fails with
[`types_stale`](errors.md#types_stale) if the file is missing or out of date.
`.dev.vars` isn't committed, so for `--check` to see the same secrets on
every machine, list their names in `.dev.vars.example` or
`secrets.required`.

`npm install` fetches `@tailworks/tailnet` (and `@tailworks/mcp`,
`@tailworks/node`) from npm, where each tailworks release publishes them at
its version (since v0.2.0). `tw dev` and `tw deploy` don't need it: for
workerd apps, tailworks resolves those imports from the copies in its own
install.

## `wrangler dev` and Miniflare

`wrangler dev` runs the same workerd. So for code that doesn't depend on
the tailnet, it is a fine inner loop. Use `tw dev` when you need anything
tailworks adds. Here is what changes under `wrangler dev`:

| | `wrangler dev` / Miniflare | `tw dev` |
|---|---|---|
| `getCaller(req)`, `getRequester(req)` | throw `TailnetError` `no_identity` ("request has no verified tailnet caller"), a 500 if the handler doesn't catch it. This is deliberate: tailworks never invents an identity | a real tailnet caller (a local dev node) |
| `fetch()` egress | leaves from your machine's own network. With tailscaled running, tailnet names resolve as **you**, not as the workload | through netd, as the workload node |
| `tailnet.objects` (a node per DO) | ignored: plain Durable Objects, no facets, no per-object egress | as deployed (facets, per-object nodes) |
| `ctx.tailnet` (`spawn`, `kill`), `@tailworks/mcp` caps | no `ctx.tailnet`: `no_identity` | ✅ |
| secrets | `.dev.vars` | `.dev.vars` ([secrets.md](secrets.md)) |
| cron triggers | `/__scheduled` (`--test-scheduled`) | `/__scheduled` with `tw dev --test-scheduled` |
| R2 (`r2_buckets`) | simulated locally | local files and an in-memory index, for one run ([r2.md](r2.md)) |
| KV (`kv_namespaces`) | simulated locally | kept between runs in `<app>/.tailworks/dev-stores.json` ([kv.md](kv.md)) |
| D1 (`d1_databases`) | simulated locally | in memory for one run; pending migrations applied at start ([d1.md](d1.md)) |
| other Cloudflare bindings | simulated locally | absent (warned) |

Code that should run under both can catch the error:
`isTailnetError(e) && e.code === "no_identity"`. Treat that as "not on
tw", never as an authenticated user. The check behind this table:
`wrangler dev` on the `hello` template answered `GET /` with that 500,
because `getCaller` threw.

## Wrangler commands that call Cloudflare

`wrangler deploy`, `wrangler secret put` and `wrangler tail` talk to
Cloudflare's API. wrangler lets you change the API's base URL
(`CLOUDFLARE_API_BASE_URL`), so in principle tailworks could serve a
Cloudflare-compatible API for them. We decided not to:

- **Surface.** wrangler 4.60 calls about 50 distinct
  `/accounts/:id/workers/…` endpoints: script upload (multipart with
  binding metadata), versions and deployments, settings, schedules,
  subdomains, routes, secrets, tails. A deploy chains several of them, in an
  order that isn't documented.
- **Churn.** wrangler ships often (4.60 to 4.162 while this was written),
  and the API it calls changes with it. Pinning a wrangler version for tailworks
  defeats the point. Not pinning means tailworks would chase every release.
- **Identity.** Cloudflare's API authenticates with bearer tokens or
  OAuth. Tailworks authenticates the caller's tailnet identity. A shim would
  either ignore the token (confusing) or bring tokens back into tailworks.
- **The tailworks parts have no place in it.** Preflighting grants and printing
  the `tagOwners` snippet, placement, building and pinning container
  images, and tailnet identity per class can't be expressed through
  wrangler's deploy flow.
- **`wrangler tail`** expects a WebSocket URL from Cloudflare's tail
  service, in Cloudflare's event format. `tw logs -f` already covers this.

Use `tw deploy`, `tw secret put` and `tw logs` instead. They take the
same project.
