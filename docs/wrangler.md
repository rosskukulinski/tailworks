# Using wrangler with twd

An existing Workers project runs on twd without changes. `twd deploy` and
`twd dev` read wrangler's own config file, bundle the project the way
wrangler does (with twd's built-in esbuild or with wrangler itself), and say
which settings they ignore.

```sh
cd my-worker            # wrangler.toml, src/index.ts, no twd files
twd dev                 # local run: twd-hostd + workerd, no tailnet
twd deploy              # to your twd stack
twd deploy --env staging
```

What twd does not use is `wrangler deploy` itself, or the other wrangler
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
- **Outbound `fetch()` reaches tailnet peers only.** Calls to the internet
  are refused (`peer_not_visible`); put what the Worker needs on your
  tailnet ([COMPAT.md](COMPAT.md#fetch)).
- **Each Durable Object is a tailnet node** with its own name, tags and
  grants. Its storage lives on one host's disk, with no replication.
- **Same CLI shapes, different binary.** `twd dev`, `twd deploy`,
  `twd secret`, `twd kv`, `twd d1`, `twd queues`, `twd r2` and `twd logs`
  stand in for their wrangler commands. Secrets apply on the next deploy.
- **Not there:** Workers AI, Vectorize, Hyperdrive, Browser Rendering,
  Email, service bindings, static assets, TCP `connect()`. The full list,
  with tests: [COMPAT.md](COMPAT.md).

## Config files

twd reads the same files as wrangler 4, in the same order. The first one
found in the app directory wins:

1. `wrangler.json`
2. `wrangler.jsonc`
3. `wrangler.toml`

If there are others, `twd deploy` prints one line for each, saying it's
ignored. Unlike wrangler, twd only looks in the app directory, not its
parents, because `twd.jsonc` sits there too.

Whatever the format, the bundle carries the resolved config as JSON
(`wrangler.jsonc`), so the host never sees TOML or environments.

## Environments (`--env`)

`twd deploy --env staging` and `twd dev --env staging` use `[env.staging]`
the way `wrangler --env staging` does:

- These keys are **inherited** from the top level unless the environment
  sets them: `main`, `compatibility_date`, `compatibility_flags`,
  `migrations`, `triggers`, `placement`, the build settings, and the rest.
- These keys are **not inherited**: `vars`, `define`, `durable_objects`,
  `containers` and every other binding. An environment without its own copy
  has none. twd prints a line for each one, as wrangler does:

  ```
  › wrangler.toml: env.staging: define is set at the top level but not in env.staging; environments don't inherit it (as in wrangler), so env.staging has none
  ```

- The **workload name** is `<name>-<env>` (for example `shop-staging`),
  unless the environment sets its own `name`. `<name>` is `twd.jsonc`
  `name` if there is one, then wrangler's `name`, then the directory name.
  `--name` overrides all of these.

So `staging` and production are two workloads with two tailnet nodes. If
`twd.jsonc` exists, it is shared by every environment. It has no `env`
section, so both get the same tags and grants.

Secrets belong to a workload, so give an environment's secrets its name:
`twd secret put API_KEY --workload shop-staging`. Run in the app
directory without `--workload`, `twd secret` uses the top-level name.

An unknown environment is an error that lists the ones that exist.

## Without a twd.jsonc

A project that has only a wrangler config gets these defaults:

```jsonc
{
  "name": "<workload>",
  "tailnet": {
    "tags": ["tag:twd-wl-<workload>"],
    // every durable_objects class:
    "objects": { "<Class>": { "node": false } }
  }
}
```

The workload node is tagged `tag:twd-wl-<workload>`. As with any new tag,
the first `twd deploy` stops at preflight and prints the `tagOwners` line to
add.

Durable Object classes are **internal**: you reach them through their
binding, as on Cloudflare, and they have no tailnet node of their own (see
[egress-modes.md](egress-modes.md)).

To give the workload `connects`, a Service or public routes, or to give a
class its own node and tags, add a `twd.jsonc` ([twd-jsonc.md](twd-jsonc.md)).
`twd deploy` says when it is using the defaults.

## Bundling

There are three ways to bundle. All three produce the same bundle shape:
the entry module plus additional modules named `<sha1>-<file>`, exactly as
wrangler names them.

| | built-in esbuild (default) | `--wrangler` | `--from-wrangler-out <dir>` |
|---|---|---|---|
| what runs | esbuild 0.28.2, linked into `twd` | `wrangler deploy --dry-run --outdir <tmp> --config <file> --env=<env>` in the app directory. Nothing is sent to Cloudflare | nothing: twd packs a directory you built with `wrangler deploy --dry-run --outdir <dir>` |
| needs Node.js | no | yes, and wrangler (`node_modules/.bin/wrangler`, then `PATH`; `$TWD_WRANGLER` overrides) | no (at deploy time) |
| use it for | most projects | custom builds, or to get exactly wrangler's output | CI that already runs wrangler, or a pinned wrangler version |

For `--from-wrangler-out`, twd finds the entry by `main`'s name
(`src/index.ts` becomes `index.js`). It leaves out `README.md` and source
maps. Each other module gets its type from your `rules` and wrangler's
default rules, matched on its file name, and otherwise from its extension.
twd doesn't check that the directory is up to date with your sources.

### What each bundler honours

Measured with `e2e/wrangler/testdata/toml-app` and wrangler 4.60.0
(`make e2e-wrangler`, `TestBuildWranglerTOMLApp`):

| wrangler setting | built-in esbuild | wrangler | notes |
|---|---|---|---|
| `main` (TS or JS) | ✅ | ✅ | |
| `define` | ✅ | ✅ | |
| `alias` | ✅ | ✅ | |
| default `rules`: `.txt` / `.html` / `.sql` as Text, `.bin` as Data, `.wasm` / `.wasm?module` as CompiledWasm | ✅ | ✅ | same `<sha1>-<file>` module names |
| custom `rules` of type Text, Data or CompiledWasm (with `fallthrough`) | ✅ | ✅ | e.g. `**/*.md` as Text. The host types each module from `twd-bundle.json` `modules` |
| `rules` of type ESModule / CommonJS, `no_bundle`, `find_additional_modules`, `base_dir`, `preserve_file_names` | ❌ warned | ✅ ES modules | twd ships ES modules only; a `.cjs` module in wrangler's output is an error |
| `build` (custom build command) | ❌ warned | ✅ | |
| `minify`, `keep_names`, `jsx_factory`, `jsx_fragment`, `tsconfig` | ✅ mapped to esbuild options | ✅ | mapped, but not measured end to end |
| `nodejs_compat` | ✅ bare built-ins become `node:*`; workerd provides them | ✅ plus wrangler's unenv polyfills | toml-app's entry: 1.0 KB built-in, 21 KB wrangler. Both pass the same checks (`node:buffer`) |
| `node_compat` (legacy) | ❌ warned | ❌ wrangler 4 rejects it | use `compatibility_flags = ["nodejs_compat"]` |
| `upload_source_maps` | ❌ warned | ❌ | twd ships no source maps |
| `assets`, `site` (static assets) | ❌ warned | ❌ | serve files from the Worker, or import them as Text/Data modules |

`create-twd`'s `build.mjs` (the npm bundler) still reads only
`wrangler.jsonc` and has none of these settings. `twd deploy` does not
use it.

## Settings twd ignores

`twd deploy` and `twd dev` print one line per top-level key they don't use,
before anything else. Nothing is ignored silently:

```
› wrangler.toml: vectorize: no such binding on twd: env.CACHE will be undefined
› wrangler.toml: routes: ignored: there is no Cloudflare zone or workers.dev; the workload answers at its MagicDNS name (twd.jsonc "public" for the internet)
› wrangler.toml: workers_dev: ignored: there is no Cloudflare zone or workers.dev; …
› no twd.jsonc: using defaults (workload node tag:twd-wl-wtoml; Durable Object classes internal, no node of their own); add a twd.jsonc to change them
```

`twd deploy --json` returns the same lines in `warnings`.

| keys | what twd says |
|---|---|
| `routes`, `route`, `workers_dev`, `preview_urls`, `account_id`, `zone_id` | ignored: the workload is reached at its MagicDNS name. Public routes are `twd.jsonc` `public` ([public-ingress.md](public-ingress.md)) |
| `services`, `ai`, `browser`, `vectorize`, `hyperdrive`, `analytics_engine_datasets`, `send_email`, `dispatch_namespaces`, `mtls_certificates`, `images`, `pipelines`, `ratelimits`, `secrets_store_secrets`, `version_metadata`, `worker_loaders`, `vpc_services`, `media`, `tail_consumers`, `logfwdr`, `unsafe` | no such binding on twd; the warning names each `env.X` that will be undefined. A section twd supports as a [platform store](design/adr/platform-stores.md) (`kv_namespaces`, [Workers KV](kv.md); `r2_buckets`, [R2](r2.md); `d1_databases`, [D1](d1.md)) is not warned about |
| `assets`, `site` | not supported |
| `observability`, `logpush`, `limits`, `usage_model`, `keep_vars`, `send_metrics`, `dev`, … | ignored by twd |
| a `durable_objects` binding with `script_name` | not supported (the deploy fails) |
| anything else | unknown key, ignored |

These keys are used without a warning: `name`, `main`, `compatibility_date`,
`compatibility_flags`, `vars`, `durable_objects`, `kv_namespaces` ([kv.md](kv.md)), `r2_buckets` ([r2.md](r2.md)), `migrations`, `triggers`
([cron-triggers.md](cron-triggers.md)), `containers`
([containers.md](containers.md)), `placement`, and the build settings above.

## `wrangler dev` and Miniflare

`wrangler dev` runs the same workerd. So for code that doesn't depend on
the tailnet, it is a fine inner loop. Use `twd dev` when you need anything
twd adds. Here is what changes under `wrangler dev`:

| | `wrangler dev` / Miniflare | `twd dev` |
|---|---|---|
| `getCaller(req)`, `getRequester(req)` | throw `TailnetError` `no_identity` ("request has no verified tailnet caller"), a 500 if the handler doesn't catch it. This is deliberate: twd never invents an identity | a real tailnet caller (a local dev node) |
| `fetch()` egress | leaves from your machine's own network. With tailscaled running, tailnet names resolve as **you**, not as the workload | through netd, as the workload node |
| `tailnet.objects` (a node per DO) | ignored: plain Durable Objects, no facets, no per-object egress | as deployed (facets, per-object nodes) |
| `ctx.tailnet` (`spawn`, `kill`), `@twd/mcp` caps | no `ctx.tailnet`: `no_identity` | ✅ |
| secrets | `.dev.vars` | `.dev.vars` ([secrets.md](secrets.md)) |
| cron triggers | `/__scheduled` (`--test-scheduled`) | `/__scheduled` with `twd dev --test-scheduled` |
| R2 (`r2_buckets`) | simulated locally | local files and an in-memory index, for one run ([r2.md](r2.md)) |
| KV (`kv_namespaces`) | simulated locally | kept between runs in `<app>/.twd/dev-stores.json` ([kv.md](kv.md)) |
| D1 (`d1_databases`) | simulated locally | in memory for one run; pending migrations applied at start ([d1.md](d1.md)) |
| other Cloudflare bindings | simulated locally | absent (warned) |

Code that should run under both can catch the error:
`isTailnetError(e) && e.code === "no_identity"`. Treat that as "not on
twd", never as an authenticated user. The check behind this table:
`wrangler dev` on the `hello` template answered `GET /` with that 500,
because `getCaller` threw.

## Wrangler commands that call Cloudflare

`wrangler deploy`, `wrangler secret put` and `wrangler tail` talk to
Cloudflare's API. wrangler lets you change the API's base URL
(`CLOUDFLARE_API_BASE_URL`), so in principle twd could serve a
Cloudflare-compatible API for them. We decided not to:

- **Surface.** wrangler 4.60 calls about 50 distinct
  `/accounts/:id/workers/…` endpoints: script upload (multipart with
  binding metadata), versions and deployments, settings, schedules,
  subdomains, routes, secrets, tails. A deploy chains several of them, in an
  order that isn't documented.
- **Churn.** wrangler ships often (4.60 to 4.162 while this was written),
  and the API it calls changes with it. Pinning a wrangler version for twd
  defeats the point. Not pinning means twd would chase every release.
- **Identity.** Cloudflare's API authenticates with bearer tokens or
  OAuth. twd authenticates the caller's tailnet identity. A shim would
  either ignore the token (confusing) or bring tokens back into twd.
- **The twd parts have no place in it.** Preflighting grants and printing
  the `tagOwners` snippet, placement, building and pinning container
  images, and tailnet identity per class can't be expressed through
  wrangler's deploy flow.
- **`wrangler tail`** expects a WebSocket URL from Cloudflare's tail
  service, in Cloudflare's event format. `twd logs -f` already covers this.

Use `twd deploy`, `twd secret put` and `twd logs` instead. They take the
same project.
