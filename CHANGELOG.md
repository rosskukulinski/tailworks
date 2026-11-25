# Changelog

What changed in each tailworks release, newest first. This file is generated
from the release catalog (`internal/catalog/releases/`, written from the
fragments in `changes/released/`); don't edit it by hand. How versions
work and what may break: [docs/RELEASING.md](docs/RELEASING.md#versions).
What may run together, and how to upgrade:
[docs/upgrading.md](docs/upgrading.md).

## v0.5.0 (2026-11-24)

workerd 1.20261121.1.

### Upgrade actions

#### deps-workerd-1.20261121.1

The pinned workerd moves from 1.20261118.1 to 1.20261121.1. `fetch()` now
rejects with a `TypeError` when a response head doesn't parse
(cloudflare/workerd#8535), `Body.formData()` rejects with the body stream's
error (#8679), `new Request(request, init)` leaves the original body usable
when it throws (#8681), and `TextDecoder` keeps its state after a fatal
streaming error (#8658). In `node:net`, connect failures to unknown hosts carry
Node's `ENOTFOUND` properties (#8648) and IDN hosts are converted to ASCII
(#8709). Apps don't need changes.

Operator action: workerd moves to 1.20261121.1.

#### policy-define-connects-tags

`tw policy` is easier to act on:

- `tw policy --apply` and `tw deploy --apply-policy` now handle a
  `tailnet.connects` target that is another tailworks app's tag before that
  app is deployed (say `tag:tw-wl-mcp`). Before, the tag had no `tagOwners`
  entry, so tailworks left the grants to it for you to add, and the snippet
  `tw policy` printed was rejected by the control plane (a grant to an
  undefined tag). Now tailworks adds the tag's `tagOwners` entry (the one
  deploying that app would add) and writes the grants; the printed snippet
  includes it too. Tenants with their own owner tag (teams) still define
  such tags themselves: the tag may be another team's.
- `tw policy --apply` prints one report: the checks, what it will add, and
  after writing either "run tw deploy" or what is still missing. It no
  longer prints the whole report a second time or shows policy ETags.
- What tailworks won't write is listed once, as a short reason, and stays in
  the snippet to paste.
- An app whose workload and object tags both need the same grant gets one
  grant with both sources, not one per tag. `tw policy` also names which
  node carries each of the app's tags.

The preflight result has a new `undefinedConnects` field.

#### readme-simplify-quickstart-tw-up

`tw up` needs no flags on a new machine. With the default state dir it reads
the OAuth client from `~/.config/tailworks/oauth.env`, and on the first run
asks for the Client Secret and saves it there (0600). It installs the login
service by default (`--service=false` to skip it); an existing state dir keeps
how it runs, so `tw upgrade` doesn't change it. It no longer asks for
confirmation (`--yes` is accepted and does nothing), and it tightens an OAuth
client file other users can read to 0600 instead of refusing it.

The `hello` template no longer declares `tailnet.connects: ["tag:db"]`, a tag
most tailnets don't have (pasting its grant failed with `tag not found`).
`tw deploy` now ends with a `curl` line for the app and `tw logs <app> -f`.
The README is rewritten as a short quick start.

#### release-rehearsal-fixes-4

The `node-workflow` template now builds from an installed release. Before,
`tw dev` and `tw deploy` failed with `Cannot find package 'esbuild'`
because create-tailworks loaded esbuild from its own install directory,
which a release doesn't include. It now uses the app's own esbuild, and the
template lists esbuild as a dependency.

Operator action: in an app made from the `node-workflow` template with an
earlier release, run `npm install --save-dev esbuild@0.28.2`.

#### service-bindings-293

wrangler `services` (service bindings) work between workloads of one
tenant: `env.AUTH.fetch()` (streaming bodies, WebSocket upgrades) and JS RPC
to the target's default `WorkerEntrypoint` or a named `entrypoint`, with
structured-clone arguments and results. The call is a tailnet call from the
calling node (the workload, or the facet object), so the target's
`getCaller()` names it (`getCaller(this.ctx)` in an RPC method), and it
needs a grant from the caller's tags to the target's like any tailnet call.
`tw types` emits each binding as `Fetcher`, and `tw deploy` no longer warns
that `env.<binding>` will be undefined. Functions, `RpcTarget` stubs and
streams can't be RPC values (`rpc_not_forwardable`); a missing target is
`service_not_found`.

A tailnet call between objects or workloads on the **same host** now works
without `tw up --allow-sibling-egress`: netd delivers it in process to the
destination's ingress, after the same netmap and packet-filter checks a call
from another host gets (so `addMcpServer` to an MCP server on the same host,
and clients of the `lock` workload on its host, work). `--allow-sibling-egress`
now only makes those calls hairpin over WireGuard.

Operator action: mgmt gains `GET /v1/tenants/{id}/workloads/{workload}/service-bindings/{binding}`
(host role). A deployment with `services` is placed only on hosts that
advertise the `service-bindings` feature, so upgrade mgmt and hosts first.

#### static-assets-278

wrangler `assets` (static files) now work. `tw deploy` and `tw dev` read
`assets.directory` and ship its files with the Worker; the host serves them
with Cloudflare's semantics: `Content-Type`, `ETag` and `304 Not Modified`,
`html_handling`, `not_found_handling` (`single-page-application`,
`404-page`, `none`), `run_worker_first` (so the Worker can check
`getCaller(req)` before a file is served) and `env.ASSETS.fetch()`. `tw dev`
reloads when a file changes. `.assetsignore` is honoured, and `_redirects`
and `_headers` work with Cloudflare's syntax (`tw` rejects a bad line
before uploading). A config with `assets` and no `main` deploys as a site
with no Worker. The files
count against the 64 MiB bundle. `tw types` types the binding as `Fetcher`.
The `assets: not supported` warning is gone. Try it with
`tw init my-site --template static-site` (docs/wrangler.md#assets).

Operator action: upgrade hosts before deploying an app with `assets`. A
bundle with `assets` needs the new `assets` host feature, so mgmt places it
only on upgraded hosts.

#### tcp-egress-294

Workers and Durable Objects can open raw TCP sockets. Stock `connect()`
from `cloudflare:sockets` now works, so database and cache drivers built for
Workers (postgres.js, and others that use `connect()`) reach a server on your
tailnet. A socket gets the identity and policy of `fetch()`:

- From a facet-mode Durable Object it leaves as the object's own node.
- From the Worker and from plain-DO objects it leaves as the workload node.

The same refusals apply (`forbidden_destination`, `peer_not_visible`,
`internet_egress_unavailable` under `tailnet.egress.internet`). `tw kill`
resets an object's open sockets. Each flow is a trace span and an `egress`
audit record (bytes and duration), and each refusal is a `deny` record.

`ctx.tailnet.connect()` is built. It returns the same socket, and a refusal
rejects `opened` with a typed `TailnetError`. `{ tls: "netd" }` has the host
do TLS to the destination. In a plain-DO object it throws `not_supported`.
`tailnet.connects` entries can name a port (`"tag:db:5432"`), so `tw deploy`
checks and prints the grant for that port. Recipe:
docs/recipes.md#connect-to-postgres-on-your-tailnet. Design: ADR-052.

Not yet: `secureTransport: "on"` / `"starttls"` on stock `connect()`
(workerd can't hand a TLS socket to tailworks' egress). On the tailnet, turn
the driver's TLS off: WireGuard already encrypts the flow. Each object has at
most 256 open sockets (`socket_limit`).

Operator action: upgrade hosts before deploying apps that use `connect()`.
On an older host `connect()` still fails.

### Features

#### doctor-readable

`tw doctor` now prints a short summary instead of one long paragraph per
check. It starts with how many checks failed, warned, passed and were
skipped. Failures and warnings come next, each with its fix, and the
passing checks are folded into one line per group (Install, Tailnet,
Storage, Isolation, and Linux host on Linux). Long lines wrap to the
terminal width, and your home directory is shown as `~`. `tw doctor -v`
prints the full detail of every check, as before. `tw doctor --json` is
unchanged, except that each check now has a `group`.

In single-process mode, missing `tw-mgmt` and `tw-hostd` binaries are now
reported as info rather than a warning, because they aren't needed there.

#### fix-295-autoapprovers

`tw policy --apply` and `tw deploy --apply-policy` now write the
`autoApprovers.services` entry a workload with `tailnet.service` needs, so
you no longer paste it into the policy by hand. The entry is as narrow as
it gets: only the workload's own `svc:` name, approved only for the tags
its node is deployed with, and only `tag:tw*` tags (`policy.Check`
refuses anything else, and the merge never changes existing
`autoApprovers` entries, routes or exit nodes). On Headscale, or when the
workload's tag isn't defined in `tagOwners`, the entry stays in the
"add it yourself" list. The `api/openapi.yaml` change is to the
description of `POST /v1/tenants/{id}/workloads/{workload}/policy` only:
no field was added or removed.

#### fix-295-custody-setec

`tw custody migrate --to setec --setec-url <url>` moves an install that
started on file custody to setec, so it can run several mgmt replicas. Run
it with tailworks stopped. It maps every file custody entry back to its
name, and refuses (`custody_unmapped`) if any entry can't be named. To name
entries written before mgmt indexed custody names, it first backfills
`custody_names` from the store's rows and the audit trail. It copies every
entry, reads each one back from setec, backs up the state, and then
switches `tw.json` to setec custody. File custody is left as it was. See
`docs/postgres.md`, "Move file custody to setec".

#### fix-295-hostd-backup

`tw backup` now also copies each workload's Durable Object storage and,
with the `fs` backend, R2 object bytes, while tailworks runs. Before, you
had to `tw down` to back these up. Each object's SQLite database is copied
with `VACUUM INTO` through a read-only connection that shares workerd's
locks, so each copy is the object as of one committed write and workerd
keeps serving. R2 blobs are immutable, so they are copied (or hard-linked
on the same disk) before and after mgmt's store. `backup.json` has a new
`hostd` entry that lists the workloads and counts the databases and blobs.
`tw restore` puts them back and moves what it replaces into the safety
backup. `tw backup --hostd=false` copies the control plane only, as
before. microVM DO volumes, Node.js workloads' data directories and other
hosts' storage are still not included; `backup.json` says so.

#### fix-295-tracing

Traces now cover Durable Object RPC, alarms and cron runs. An RPC call
(`stub.method(...)`) gets a CLIENT span in the caller and a SERVER span in
the object, in the caller's trace. Each alarm and each cron run starts its
own trace, so their records in `tw logs --source requests` now carry a trace
ID. Egress, `ctx.tailnet.fetch()` and DO calls made inside them join that
trace. What is still not traced is listed in `docs/observability.md`.

#### fix-295-worker-internet

`tw up --worker-internet=off` turns off Workers' internet egress for the
whole host, as `tw-hostd -worker-internet=off` already could. The default
is `direct`: Workers' `fetch()` reaches public internet hosts through the
host's network, narrowed by each workload's `tailnet.egress.internet`. The
setting is saved in `tw.json`, so `tw down` / `tw up`, a service restart
and `tw upgrade` keep it; give the flag again to change it. `tw status`
shows it on a new `internet` line (and as `workerInternet` in
`tw status --json`), and `tw doctor` has a `worker_internet` check that
warns when the running host still runs a different setting than `tw.json`.

#### fix-301

Projects built with the Cloudflare Vite plugin (`@cloudflare/vite-plugin`:
React Router, the React and Workflows starters, Agents SDK examples) now
deploy with `npm run build && tw deploy`. Like `wrangler deploy`, `tw
deploy`, `tw dev`, `tw policy` and `tw types` follow
`.wrangler/deploy/config.json` to the config the build wrote, print one line
saying so, and keep `tailworks.jsonc` from the project directory. The
built-in bundler packs a pre-built `no_bundle` Worker (ES-module `rules` for
only `.js` / `.mjs`, as the Vite plugin writes) as it is, instead of failing
with `build_failed`; `--wrangler` is no longer needed for these projects
(docs/wrangler.md#vite).

#### implement-issue-240

The tw CLI is on npm (issue #240): `npx tailworks init hello && cd hello && npx tailworks dev`, or `npm install -g tailworks` for `tw` (and `tailworks`) on your PATH. The `tailworks` package is a small shim. npm installs it with one platform package, `@tailworks/cli-darwin-arm64`, `@tailworks/cli-linux-arm64` or `@tailworks/cli-linux-amd64`. Each is made from the signed release tarball and published with provenance: the same `tw`, daemons, workerd and bundler, without the microVM guest. State stays in `~/.tailworks`.

On an npm install, `tw upgrade` refuses with the new `npm_install` error and says to upgrade with npm (`tw down && npm install -g tailworks@latest && tw up`). `tw version --check` prints that command, and `tw-hostd -self-upgrade` refuses. `tw up` from `npx` runs without a login service, because npm can clear npx's cache, and `tw up --service` from there refuses. `tw doctor` has a new "installed by" check (install.sh, npm or npx), and on Linux its microVM check says the npm package has no guest. The README quick start now starts with npm. `scripts/get.sh` remains the way to install without Node.js, and the only one with the microVM tier.

#### init-existing-tags

`tw init` can now use tags your tailnet policy already has, instead of
naming new ones after the app. `--tags tag:tw-app` sets the workload
node's tags, and `--object-tags tag:tw-agent` sets the tags of every
Durable Object class with its own node (`--object-tags <Class>=tag:a` sets
one class). A new app then needs no policy change, which helps when someone
else manages the policy file. `tw init` checks the tags before it writes
anything, and refuses tailworks' own tags (`reserved_tag`).

#### pages-functions-issue-285

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

#### readme-simplify-quickstart-tw-up

`tw up` needs no flags on a new machine. With the default state dir it reads
the OAuth client from `~/.config/tailworks/oauth.env`, and on the first run
asks for the Client Secret and saves it there (0600). It installs the login
service by default (`--service=false` to skip it); an existing state dir keeps
how it runs, so `tw upgrade` doesn't change it. It no longer asks for
confirmation (`--yes` is accepted and does nothing), and it tightens an OAuth
client file other users can read to 0600 instead of refusing it.

The `hello` template no longer declares `tailnet.connects: ["tag:db"]`, a tag
most tailnets don't have (pasting its grant failed with `tag not found`).
`tw deploy` now ends with a `curl` line for the app and `tw logs <app> -f`.
The README is rewritten as a short quick start.

#### require-exit-node-egress-hosts-workloads

A host can send every workload's internet egress through a tailnet exit
node: `tw up --worker-internet=exit-node=<node>` (or `tw-hostd
-worker-internet=exit-node=<node> -container-internet=exit-node`). The
host's own node (`tag:tw-host`) uses the exit node, so the destination
sees the exit node's address and the host's resolver never sees the names
(they resolve at the exit node, or at `dns=<ip>` through it). netd's SSRF
guard is unchanged. When the exit node is missing, not offered, offline or
refuses DNS, internet egress is refused with the new `exit_node_unavailable`
code; the host never falls back to its own network. `tw status` and
`tw doctor` show the exit node and why it is unusable.

A workload can require it with `tailnet.egress.via: "exit-node"` in
tailworks.jsonc. Such a deploy needs the new host feature
`internet-exit-node`, which a host advertises only when its path is an
exit node; mgmt places it only there (`feature_unavailable` otherwise).

Operator action: none unless you use it. To use it, grant `tag:tw-host` →
`autogroup:internet` in the tailnet policy and approve the exit node's
routes.

#### service-bindings-293

wrangler `services` (service bindings) work between workloads of one
tenant: `env.AUTH.fetch()` (streaming bodies, WebSocket upgrades) and JS RPC
to the target's default `WorkerEntrypoint` or a named `entrypoint`, with
structured-clone arguments and results. The call is a tailnet call from the
calling node (the workload, or the facet object), so the target's
`getCaller()` names it (`getCaller(this.ctx)` in an RPC method), and it
needs a grant from the caller's tags to the target's like any tailnet call.
`tw types` emits each binding as `Fetcher`, and `tw deploy` no longer warns
that `env.<binding>` will be undefined. Functions, `RpcTarget` stubs and
streams can't be RPC values (`rpc_not_forwardable`); a missing target is
`service_not_found`.

A tailnet call between objects or workloads on the **same host** now works
without `tw up --allow-sibling-egress`: netd delivers it in process to the
destination's ingress, after the same netmap and packet-filter checks a call
from another host gets (so `addMcpServer` to an MCP server on the same host,
and clients of the `lock` workload on its host, work). `--allow-sibling-egress`
now only makes those calls hairpin over WireGuard.

Operator action: mgmt gains `GET /v1/tenants/{id}/workloads/{workload}/service-bindings/{binding}`
(host role). A deployment with `services` is placed only on hosts that
advertise the `service-bindings` feature, so upgrade mgmt and hosts first.

#### static-assets-278

wrangler `assets` (static files) now work. `tw deploy` and `tw dev` read
`assets.directory` and ship its files with the Worker; the host serves them
with Cloudflare's semantics: `Content-Type`, `ETag` and `304 Not Modified`,
`html_handling`, `not_found_handling` (`single-page-application`,
`404-page`, `none`), `run_worker_first` (so the Worker can check
`getCaller(req)` before a file is served) and `env.ASSETS.fetch()`. `tw dev`
reloads when a file changes. `.assetsignore` is honoured, and `_redirects`
and `_headers` work with Cloudflare's syntax (`tw` rejects a bad line
before uploading). A config with `assets` and no `main` deploys as a site
with no Worker. The files
count against the 64 MiB bundle. `tw types` types the binding as `Fetcher`.
The `assets: not supported` warning is gone. Try it with
`tw init my-site --template static-site` (docs/wrangler.md#assets).

Operator action: upgrade hosts before deploying an app with `assets`. A
bundle with `assets` needs the new `assets` host feature, so mgmt places it
only on upgraded hosts.

#### tcp-egress-294

Workers and Durable Objects can open raw TCP sockets. Stock `connect()`
from `cloudflare:sockets` now works, so database and cache drivers built for
Workers (postgres.js, and others that use `connect()`) reach a server on your
tailnet. A socket gets the identity and policy of `fetch()`:

- From a facet-mode Durable Object it leaves as the object's own node.
- From the Worker and from plain-DO objects it leaves as the workload node.

The same refusals apply (`forbidden_destination`, `peer_not_visible`,
`internet_egress_unavailable` under `tailnet.egress.internet`). `tw kill`
resets an object's open sockets. Each flow is a trace span and an `egress`
audit record (bytes and duration), and each refusal is a `deny` record.

`ctx.tailnet.connect()` is built. It returns the same socket, and a refusal
rejects `opened` with a typed `TailnetError`. `{ tls: "netd" }` has the host
do TLS to the destination. In a plain-DO object it throws `not_supported`.
`tailnet.connects` entries can name a port (`"tag:db:5432"`), so `tw deploy`
checks and prints the grant for that port. Recipe:
docs/recipes.md#connect-to-postgres-on-your-tailnet. Design: ADR-052.

Not yet: `secureTransport: "on"` / `"starttls"` on stock `connect()`
(workerd can't hand a TLS socket to tailworks' egress). On the tailnet, turn
the driver's TLS off: WireGuard already encrypts the flow. Each object has at
most 256 open sockets (`socket_limit`).

Operator action: upgrade hosts before deploying apps that use `connect()`.
On an older host `connect()` still fails.

#### ui-console-workload-node-logs

The admin UI now shows logs. A workload's logs page
(`/ui/tenants/<tenant>/workloads/<workload>/logs`, linked from the
deployment, tenant and overview pages) has two tabs: **Requests** shows the
request records newest first, with each request's `console.*` lines and
exceptions, and filters by node, status, caller, trace and errors only.
**Console output** shows the workload's raw output. Each node (object) page
links to `/ui/objects/<id>/logs`, which shows that node's records. The pages
refresh every 5 s, so they follow the log, and they work for workloads with
replicas. They read the same `GET /v1/…/logs` as `tw logs`, so there is no
API change.

### Fixes

#### cloudflare-examples-compat-suite

`tw deploy --wrangler` (and `tw dev --wrangler`) now runs wrangler with the
project's `node_modules/.bin` first on PATH, as `npm run deploy` does. A
wrangler `build.command` that calls a locally installed tool (for example
`esbuild …` in Cloudflare's `durable-chat-template`) used to fail with
`command not found`.

A Python Worker (`main: *.py` or the `python_workers` flag) is now refused
with `not_supported: Python Workers are not supported`, instead of
esbuild's "No loader is configured for .py files".

New: `make e2e-compat` deploys 30 of Cloudflare's own example projects,
unmodified, to a real host on a real tailnet and calls each one as its
README does (`e2e/compat/README.md`).

#### deps-workerd-1.20261121.1

The pinned workerd moves from 1.20261118.1 to 1.20261121.1. `fetch()` now
rejects with a `TypeError` when a response head doesn't parse
(cloudflare/workerd#8535), `Body.formData()` rejects with the body stream's
error (#8679), `new Request(request, init)` leaves the original body usable
when it throws (#8681), and `TextDecoder` keeps its state after a fatal
streaming error (#8658). In `node:net`, connect failures to unknown hosts carry
Node's `ENOTFOUND` properties (#8648) and IDN hosts are converted to ASCII
(#8709). Apps don't need changes.

Operator action: workerd moves to 1.20261121.1.

#### fix-268

The `ai-agent` template now works when you follow its README. Its default
`vars.MCP_URL` was `http://mcp/mcp`, which named no node: a workload's node
is `tw-<workload>` (with `tw up`'s default `--hostname-prefix`), so the
agent got `peer_not_visible` even with the `mcp` template deployed. The
default is now `http://tw-mcp/mcp`, and the `node-workflow` README uses the
same address. The README walks through the whole two-workload setup:
deploying `mcp` under that name, the capability grant that gives the agent
its tools, what a working reply looks like, and that the scripted model
ignores the prompt. `tw init --template ai-agent` and the template listings
say it needs `mcp`. Apps already made from the template keep their old
`MCP_URL`: set it to `http://tw-mcp/mcp` (or `tw deploy --var MCP_URL=…`).

#### fix-288-early-response

A Worker or Durable Object that answers a request without reading its body
(a killed object's 410, a handler that rejects an upload with 401 or 413)
now reaches the caller with that answer. Before, when the body was about
16 KB or more, the caller often got `503 upstream_unavailable` ("write:
broken pipe" or "socket is not connected") instead.

#### fix-303

A Worker's `fetch`, a Durable Object and a Workflow no longer see the
internal `x-tw-object` header in `request.headers`. netd sets it so hostd
can route the request to the right object; hostd now removes it (and any
other internal `x-tw-*` routing header) before the request reaches tenant
code, so apps that log, sign or forward all request headers no longer carry
a tailworks object ID. Use `getCaller` and `request.cf` for the caller.

#### policy-define-connects-tags

`tw policy` is easier to act on:

- `tw policy --apply` and `tw deploy --apply-policy` now handle a
  `tailnet.connects` target that is another tailworks app's tag before that
  app is deployed (say `tag:tw-wl-mcp`). Before, the tag had no `tagOwners`
  entry, so tailworks left the grants to it for you to add, and the snippet
  `tw policy` printed was rejected by the control plane (a grant to an
  undefined tag). Now tailworks adds the tag's `tagOwners` entry (the one
  deploying that app would add) and writes the grants; the printed snippet
  includes it too. Tenants with their own owner tag (teams) still define
  such tags themselves: the tag may be another team's.
- `tw policy --apply` prints one report: the checks, what it will add, and
  after writing either "run tw deploy" or what is still missing. It no
  longer prints the whole report a second time or shows policy ETags.
- What tailworks won't write is listed once, as a short reason, and stays in
  the snippet to paste.
- An app whose workload and object tags both need the same grant gets one
  grant with both sources, not one per tag. `tw policy` also names which
  node carries each of the app's tags.

The preflight result has a new `undefinedConnects` field.

#### release-rehearsal-fixes-4

The `node-workflow` template now builds from an installed release. Before,
`tw dev` and `tw deploy` failed with `Cannot find package 'esbuild'`
because create-tailworks loaded esbuild from its own install directory,
which a release doesn't include. It now uses the app's own esbuild, and the
template lists esbuild as a dependency.

Operator action: in an app made from the `node-workflow` template with an
earlier release, run `npm install --save-dev esbuild@0.28.2`.

## v0.4.0 (2026-11-23)

workerd 1.20261118.1.

### Breaking

#### feat-161-worker-internet-egress

Workers now have internet egress, on by default, as on Cloudflare
(ADR-049, #161). A Worker's, Durable Object's or Node
app's `fetch("https://api.example.com/...")` now works instead of failing
with `internet_egress_unavailable`. The request leaves from the host's own
network, not from a tailnet node, so the API sees the host's address (each
host has its own) and no tailnet identity. Tailnet egress is unchanged.

An SSRF guard always applies: loopback, private (RFC 1918, ULA),
link-local (including cloud metadata at `169.254.169.254`), CGNAT and
tailnet ranges and the host's own addresses are refused with
`forbidden_destination`. The check is on the address actually dialled,
so a DNS name that resolves to any such address is refused too. Names
under `.internal`, `.local`, `.localhost`, `.lan` and other special-use
suffixes are never looked up. The first internet flow per object and
destination is an `egress` audit record.

`tailnet.egress.internet` in tailworks.jsonc is now one setting for
Workers and containers: `false` (or `[]`) turns internet egress off, a
list of hosts limits it, and `true` also lets containers started with
`enableInternet` reach any public host. When it is absent, Workers may
reach any public host and containers none, as before.
`internet_egress_unavailable` now means internet egress is turned off by
config. `tw-hostd` has a new `-worker-internet` flag (`direct`, the
default, or `off`), and hosts advertise the `worker-internet` feature,
which a bundle that uses `true` or `false` needs.

Operator action: workloads without `tailnet.egress.internet` gain internet
egress as soon as their host runs this release, without a redeploy. To
keep a workload off the internet, set `"egress": {"internet": false}` (or
`[]` while some hosts are still on an older release) and redeploy it
before upgrading, or run `tw-hostd -worker-internet=off`. Workloads with an
allowlist keep it for containers, and their Workers may now reach the
listed hosts.

### Security

#### fix-148-seal-cf

A caller in `request.cf` is now sealed. hostd seals the caller of every
tailnet request with an HMAC under the instance's key, and the platform
checks the seal before a Durable Object, the Worker or an audit
`actingFor` uses it. Tenant code that forwards a request in-process with a
caller it made up (a Durable Object stub `fetch`, or a `Request` passed by
RPC to a facet object) now delivers no caller, so `getCaller()` throws
`no_identity`. Forwarding the request you received still carries its
caller. A seal lasts five minutes and can be replayed only for the same
real caller, inside the same workload (RT3-08 / H08, #148).

#### fix-151-vmhelper-base

tw-vmhelper now decides for itself which microVM may become a base
snapshot. hostd marks the VM it boots for the base (`StartRequest.Base`),
and the helper refuses `snapshot-base` on every other VM (a claimed pool
VM, a VM with a Durable Object volume, a restored copy) and refuses to
attach a volume to the base VM. Before, it accepted any VM that still had
a free volume slot, which included a claimed workload without a volume.

Upgrade: the field is additive and the helper protocol stays at 2. A newer
hostd with an older helper works as before (the helper ignores the field).
An older hostd with a newer helper gets its `snapshot-base` refused, logs
"microVM base snapshot failed; booting instead of restoring", and boots
VMs cold until hostd is upgraded too. Only hosts with microVM snapshots
turned on are affected.

#### fix-156-placement-poison

Smart placement bounds what a host's path report can claim. A huge `rttMs`
or `ageMs` used to overflow, so a host could make its own path look like the
best one, or keep a measurement fresh for ever. mgmt now clamps a reported
RTT to 1 minute and an age to 1 hour, and placement ignores stored
measurements outside those bounds. `docs/security-model.md` now says what a
compromised host's path reports can and can't do (ledger H23).

#### fix-158-warm-keys

Warm keys (pre-minted, preauthorized single-use join keys that make new
objects start faster) no longer outlive the mgmt that holds them. `tw down`
now revokes every unused warm key with Tailscale and deletes it from mgmt's
custody as mgmt stops (before, only `tw down --purge` did; otherwise they
stayed in custody until they expired, up to 30 min). `tw tenant rm` already
revoked them. `tw status` shows how many warm keys each tenant has in
custody, and `GET /v1/tenants/{id}` has a new `warmKeys` count (also in
`tw status --json`).

Two narrow leaks are closed too: a warm key being minted when mgmt shut
down, and one minted while `tw tenant rm` ran, could stay live at Tailscale
until it expired. Both are now revoked.

tw-mgmt has a new flag, `-revoke-warm-keys-on-exit` (off by default; `tw up`
sets it). Leave it off for mgmt replicas that share a store, or every
replica restart revokes the shared pool.

Operator action: none. After a `tw up` restart (including `tw upgrade`) the
pool refills on the next create or deploy.

#### fix-175-seatbelt-reads

On macOS, the process-tier Seatbelt profile now allows reads only from the
OS dirs (`/System`, `/usr`, `/bin`, `/sbin`, `/etc`, `/opt`, `/dev`, the
time zone database), the runtime binary, the Node install prefix and the
workload's own dirs. Before, a workload could read anything your user can
read outside a deny list (`/tmp`, `/var/folders`, shared dirs, other
users' world-readable files). Its TCP rules are also IPv4-only now, so it
reaches its egress proxy and its own port on `127.0.0.1` but not on `[::1]`.

A Node app that reads files outside its own dirs (for example under `/tmp`
or `/Library`), or listens on `localhost` (which Node resolves to `::1`)
instead of `$HOST`, now gets `EPERM`. `tw-hostd -sandbox seatbelt-legacy`
still runs the old files-only profile for one release.

#### fix-185-154-ingress-controls

Public ingress refuses paths with control characters (`%00`–`%1F`, `%7F`)
with `400 bad_request`, counted in the `public_rejected` summary like other
bad paths, so NUL, ESC and CR/LF never reach your app. New object names
containing a C0 control, DEL, a C1 control or a bidi control are refused
with the new error code `400 bad_object_name` (hostd ensure/admit and mgmt
`POST /v1/objects`). Objects that already have such a name are not renamed
or deleted. `docs/public-ingress.md` now says that apps receive the path
decoded once and should not decode it again.

Operator action: none. In a mixed-version fleet, an upgraded hostd or an
upgraded mgmt is enough to refuse such names.

#### fix-211-killed-do-rpc

A killed plain Durable Object (`"egress": "workload"`) no longer runs tenant
code through its RPC methods. Before, only its `fetch` checked the kill, so
a stub kept from before `tw kill`, or the raw binding from
`import { env } from "cloudflare:workers"`, could still call its methods.
Now every method or getter reached from outside the object fails with
`[killed]` (the caller's request gets `410 killed`), and its `alarm` and
WebSocket handlers do nothing. A facet object's platform host refuses to
start a killed object's code by any path.

#### fix-facet-host-rpc

A facet object's platform host no longer answers tenant code on its
internal methods. The Worker's raw binding (`import { env } from
"cloudflare:workers"`) and a facet's bindings both reach that host, and
workerd exposes a Durable Object's methods over RPC. Its helpers are no
longer methods, and the calls only the platform makes (kill, container
intercept, alarm delegation) now need a key only the platform holds, so
tenant code can no longer start an object hostd did not admit, set its
alarm, or kill it without `tw kill`.

#### fix-lan-bind-exposure

A sandboxed Node app can now be reached only through tailworks' ingress.
Before, an app that ignored `HOST` and called `listen(PORT)` (which binds
every interface), or that opened its own listener on `0.0.0.0`, could be
reached on its port at the host's LAN address, without WireGuard identity
or tailnet ACLs. That was possible on both macOS and Linux.

hostd now owns the app's listener. On Linux it passes `127.0.0.1:<PORT>`
to the app as fd 3, and a seccomp rule makes any other `listen()` a no-op.
On macOS the app listens on a unix socket in its instance dir, and hostd
forwards `127.0.0.1:<PORT>` to that socket for self-calls. A preload points
`server.listen(PORT)` at hostd's listener, so apps need no change.

Behaviour change: under a sandbox, a listen on any port other than `PORT`
(or a native module's own TCP listener) now fails. On Linux kernels with
Landlock ABI < 4 it returns success instead, but the socket never accepts
connections.

#### sec-155-svc-caps

App capabilities on calls through a Tailscale Service now match the policy
(H22, #155). Before, a workload (and `tw-mgmt` behind `svc:tw-mgmt`) worked
out a caller's caps as if the call had gone to the node's own address. So on
a call through the Service VIP, `caps()` was missing the caps of grants to
the `svc:` name, and it showed the caps of grants to the workload's tags even
though the caller hadn't dialed the node. Now a call through the VIP gets
exactly the caps of grants whose `dst` is the `svc:` name. A call to the
node's own name or address gets the caps of grants to its tags, as before.

`tw deploy` and `tw policy` warn (`! preflight  access:`) for each caller
whose caps are on the workload tags but not on its `svc:` name, and print the
grant to add. `tw policy` also prints the caller grant to the `svc:` name
(`serviceCallerGrant` in `--json`).

Operator action: this can take away caps that callers of a Service used to
get. If a caller of a workload behind a Service gets its app capabilities
from a grant to the workload's tags, for example

    {"src": ["tag:agent"], "dst": ["tag:tw-wl-support"], "app": {"example.com/cap/mcp": [{"tools": ["lookup_order"]}]}}

then add the same `app` to its grant to the Service, or add one:

    {"src": ["tag:agent"], "dst": ["svc:support"], "ip": ["tcp:80"], "app": {"example.com/cap/mcp": [{"tools": ["lookup_order"]}]}}

Run `tw policy` in the app's directory to list the grants to add (`tw deploy`
prints the same warnings). For HA mgmt behind `svc:tw-mgmt`, the admin cap must be on the
grant to `svc:tw-mgmt`, as `docs/services.md` already shows:

    {"src": ["autogroup:admin"], "dst": ["svc:tw-mgmt"], "ip": ["tcp:80"], "app": {"github.com/rosskukulinski/tailworks/cap/admin": [{}]}}

Without it, admins who reach mgmt through the Service are no longer admins.
Workloads that aren't behind a Service, and single-mgmt `tw up`
installs (admins reach `tag:tw-mgmt` by node name), are unchanged.

### Upgrade actions

#### bench-114-node-density

A host's default node capacity now comes from its RAM: a quarter of it at
8 MiB per object node, at least 50 and at most 1000. Before, every host
advertised 1000. That figure is twice the measured cost of a node under
request load. Per-node memory and registration rates on Linux and macOS
are published in docs/limits.md ("Per-node memory" and "Registration
rate"), and `make bench` reproduces them. docs/INSTALL.md has a sizing
section.

Operator action: a host with less than 32 GiB of RAM now advertises fewer
than 1000 nodes (an 8 GiB host 256, a 16 GiB host 512). To keep the old
figure, pass `tw-hostd -capacity-nodes 1000`.

#### feat-115-limits

Process-tier workloads now have a V8 heap cap: each isolate in a workerd
(the Worker and each Durable Object class) is capped at 1024 MiB
(`tw-hostd -workerd-heap-mib`), and a Node-tier app runs with
`--max-old-space-size=2048` (`tw-hostd -node-heap-mib`) unless its
`node.args` or `NODE_OPTIONS` set their own. Past the cap the workload's
process aborts and hostd restarts it, so one runaway allocation can't take
the host's memory. `-1` turns either cap off.

The mgmt API rate-limits its tailnet callers: each WhoIs node may make 100
requests/s (bursts of 200), each host 1000/s (bursts of 2000), and non-host
callers together hold at most 256 requests at once. Over a limit is the new
error code `429 rate_limited` with `Retry-After`. mgmt's own node (the
operator console) and the local socket are not limited. Configure with
`tw-mgmt -api-rps`, `-api-host-rps` and `-api-inflight` (`-1` = off).

`docs/security-model.md` now states what availability isolation each tier
gives (the process tier gives none beyond the heap cap), and
`docs/limits.md` gives the reason for every limit tailworks deliberately
doesn't set.

Operator action: none, unless a workload legitimately needs a V8 heap over
1 GiB (raise `-workerd-heap-mib`) or a script drives the API faster than
100 requests/s from one node (raise `-api-rps`).

#### feat-116-policy-apply

`tw policy --apply` and `tw deploy --apply-policy` write an app's missing
policy lines to the tailnet policy for you, so you no longer have to paste
them into the admin console. That covers the `tagOwners` of its tags, the
`tailnet.connects` grants, and `tag:tw-public` with its funnel `nodeAttrs`.
tw prints the lines first and asks before writing; pass `--yes` when there
is no terminal. mgmt then merges in only what is missing. Comments are kept,
nothing is changed or removed, and nothing outside `tag:tw*` is written.
It validates the result and writes it with `If-Match`. If the policy changed
in the meantime, it retries once, then fails with `policy_conflict`. Each
write is audited as `policy_apply`. The new route is
`POST /v1/tenants/{id}/workloads/{workload}/policy`. It is for admins only,
and `dryRun=true` gives a plan without writing. The new error codes are
`policy_conflict` and `policy_write_denied`. `tw deploy --json` gains a
`policy` event.

Operator action: to use it, the tenant's OAuth client needs the Policy File
write scope. If it doesn't have that scope, paste the snippet `tw policy`
prints, as before.

#### feat-161-worker-internet-egress

Workers now have internet egress, on by default, as on Cloudflare
(ADR-049, #161). A Worker's, Durable Object's or Node
app's `fetch("https://api.example.com/...")` now works instead of failing
with `internet_egress_unavailable`. The request leaves from the host's own
network, not from a tailnet node, so the API sees the host's address (each
host has its own) and no tailnet identity. Tailnet egress is unchanged.

An SSRF guard always applies: loopback, private (RFC 1918, ULA),
link-local (including cloud metadata at `169.254.169.254`), CGNAT and
tailnet ranges and the host's own addresses are refused with
`forbidden_destination`. The check is on the address actually dialled,
so a DNS name that resolves to any such address is refused too. Names
under `.internal`, `.local`, `.localhost`, `.lan` and other special-use
suffixes are never looked up. The first internet flow per object and
destination is an `egress` audit record.

`tailnet.egress.internet` in tailworks.jsonc is now one setting for
Workers and containers: `false` (or `[]`) turns internet egress off, a
list of hosts limits it, and `true` also lets containers started with
`enableInternet` reach any public host. When it is absent, Workers may
reach any public host and containers none, as before.
`internet_egress_unavailable` now means internet egress is turned off by
config. `tw-hostd` has a new `-worker-internet` flag (`direct`, the
default, or `off`), and hosts advertise the `worker-internet` feature,
which a bundle that uses `true` or `false` needs.

Operator action: workloads without `tailnet.egress.internet` gain internet
egress as soon as their host runs this release, without a redeploy. To
keep a workload off the internet, set `"egress": {"internet": false}` (or
`[]` while some hosts are still on an older release) and redeploy it
before upgrading, or run `tw-hostd -worker-internet=off`. Workloads with an
allowlist keep it for containers, and their Workers may now reach the
listed hosts.

#### fix-158-warm-keys

Warm keys (pre-minted, preauthorized single-use join keys that make new
objects start faster) no longer outlive the mgmt that holds them. `tw down`
now revokes every unused warm key with Tailscale and deletes it from mgmt's
custody as mgmt stops (before, only `tw down --purge` did; otherwise they
stayed in custody until they expired, up to 30 min). `tw tenant rm` already
revoked them. `tw status` shows how many warm keys each tenant has in
custody, and `GET /v1/tenants/{id}` has a new `warmKeys` count (also in
`tw status --json`).

Two narrow leaks are closed too: a warm key being minted when mgmt shut
down, and one minted while `tw tenant rm` ran, could stay live at Tailscale
until it expired. Both are now revoked.

tw-mgmt has a new flag, `-revoke-warm-keys-on-exit` (off by default; `tw up`
sets it). Leave it off for mgmt replicas that share a store, or every
replica restart revokes the shared pool.

Operator action: none. After a `tw up` restart (including `tw upgrade`) the
pool refills on the next create or deploy.

#### sec-155-svc-caps

App capabilities on calls through a Tailscale Service now match the policy
(H22, #155). Before, a workload (and `tw-mgmt` behind `svc:tw-mgmt`) worked
out a caller's caps as if the call had gone to the node's own address. So on
a call through the Service VIP, `caps()` was missing the caps of grants to
the `svc:` name, and it showed the caps of grants to the workload's tags even
though the caller hadn't dialed the node. Now a call through the VIP gets
exactly the caps of grants whose `dst` is the `svc:` name. A call to the
node's own name or address gets the caps of grants to its tags, as before.

`tw deploy` and `tw policy` warn (`! preflight  access:`) for each caller
whose caps are on the workload tags but not on its `svc:` name, and print the
grant to add. `tw policy` also prints the caller grant to the `svc:` name
(`serviceCallerGrant` in `--json`).

Operator action: this can take away caps that callers of a Service used to
get. If a caller of a workload behind a Service gets its app capabilities
from a grant to the workload's tags, for example

    {"src": ["tag:agent"], "dst": ["tag:tw-wl-support"], "app": {"example.com/cap/mcp": [{"tools": ["lookup_order"]}]}}

then add the same `app` to its grant to the Service, or add one:

    {"src": ["tag:agent"], "dst": ["svc:support"], "ip": ["tcp:80"], "app": {"example.com/cap/mcp": [{"tools": ["lookup_order"]}]}}

Run `tw policy` in the app's directory to list the grants to add (`tw deploy`
prints the same warnings). For HA mgmt behind `svc:tw-mgmt`, the admin cap must be on the
grant to `svc:tw-mgmt`, as `docs/services.md` already shows:

    {"src": ["autogroup:admin"], "dst": ["svc:tw-mgmt"], "ip": ["tcp:80"], "app": {"github.com/rosskukulinski/tailworks/cap/admin": [{}]}}

Without it, admins who reach mgmt through the Service are no longer admins.
Workloads that aren't behind a Service, and single-mgmt `tw up`
installs (admins reach `tag:tw-mgmt` by node name), are unchanged.

### Features

#### bench-114-node-density

A host's default node capacity now comes from its RAM: a quarter of it at
8 MiB per object node, at least 50 and at most 1000. Before, every host
advertised 1000. That figure is twice the measured cost of a node under
request load. Per-node memory and registration rates on Linux and macOS
are published in docs/limits.md ("Per-node memory" and "Registration
rate"), and `make bench` reproduces them. docs/INSTALL.md has a sizing
section.

Operator action: a host with less than 32 GiB of RAM now advertises fewer
than 1000 nodes (an 8 GiB host 256, a 16 GiB host 512). To keep the old
figure, pass `tw-hostd -capacity-nodes 1000`.

#### feat-115-limits

Process-tier workloads now have a V8 heap cap: each isolate in a workerd
(the Worker and each Durable Object class) is capped at 1024 MiB
(`tw-hostd -workerd-heap-mib`), and a Node-tier app runs with
`--max-old-space-size=2048` (`tw-hostd -node-heap-mib`) unless its
`node.args` or `NODE_OPTIONS` set their own. Past the cap the workload's
process aborts and hostd restarts it, so one runaway allocation can't take
the host's memory. `-1` turns either cap off.

The mgmt API rate-limits its tailnet callers: each WhoIs node may make 100
requests/s (bursts of 200), each host 1000/s (bursts of 2000), and non-host
callers together hold at most 256 requests at once. Over a limit is the new
error code `429 rate_limited` with `Retry-After`. mgmt's own node (the
operator console) and the local socket are not limited. Configure with
`tw-mgmt -api-rps`, `-api-host-rps` and `-api-inflight` (`-1` = off).

`docs/security-model.md` now states what availability isolation each tier
gives (the process tier gives none beyond the heap cap), and
`docs/limits.md` gives the reason for every limit tailworks deliberately
doesn't set.

Operator action: none, unless a workload legitimately needs a V8 heap over
1 GiB (raise `-workerd-heap-mib`) or a script drives the API faster than
100 requests/s from one node (raise `-api-rps`).

#### feat-116-policy-apply

`tw policy --apply` and `tw deploy --apply-policy` write an app's missing
policy lines to the tailnet policy for you, so you no longer have to paste
them into the admin console. That covers the `tagOwners` of its tags, the
`tailnet.connects` grants, and `tag:tw-public` with its funnel `nodeAttrs`.
tw prints the lines first and asks before writing; pass `--yes` when there
is no terminal. mgmt then merges in only what is missing. Comments are kept,
nothing is changed or removed, and nothing outside `tag:tw*` is written.
It validates the result and writes it with `If-Match`. If the policy changed
in the meantime, it retries once, then fails with `policy_conflict`. Each
write is audited as `policy_apply`. The new route is
`POST /v1/tenants/{id}/workloads/{workload}/policy`. It is for admins only,
and `dryRun=true` gives a plan without writing. The new error codes are
`policy_conflict` and `policy_write_denied`. `tw deploy --json` gains a
`policy` event.

Operator action: to use it, the tenant's OAuth client needs the Policy File
write scope. If it doesn't have that scope, paste the snippet `tw policy`
prints, as before.

#### feat-120-backup-migrate

`tw backup [--out <dir>]` backs up a running install: a consistent copy of
mgmt's SQLite store, custody, the ID secret, mgmt's node state and
`tw.json`, with a `backup.json` that says what is and isn't included.
`tw restore <dir>` puts a backup (or a `tw upgrade` backup) back with
tailworks stopped. It keeps the state it replaces and runs the restore
fence.

`tw-mgmt migrate --from sqlite --to-file <dsn file>` copies a stopped
install's SQLite store into an empty Postgres database in one transaction,
then fences it. `tw up --db-file <file>` (or `--db`) runs mgmt on that
store, and `--custody setec --setec-url` uses setec custody on a new
install. docs/postgres.md covers when to switch, backups with Postgres, and
HA replicas, which still run `tw-mgmt` directly.

A store object on a host whose copy is ahead of mgmt's journal (after a
restore or an upgrade's rollback) now starts over from the journal.
Before, its writes kept failing with `store_conflict`.

#### feat-120-quotas-retention

Per-tenant storage quotas, usage, disk checks and retention (#120).

- `tw tenant set --max-store-bytes 20GiB --max-queue-bytes 2GiB --max-stores 50`
  bounds what one tenant keeps in mgmt: all its KV, D1 and R2 journals, all
  its queued messages, and how many stores it has. A write past a quota fails
  `429 storage_quota_exceeded`; deletes and overwrites that don't grow a store
  still work. The quotas are unlimited unless set (`-` clears one).
- `tw tenant ls` shows each tenant's storage usage against its quotas
  (`GET /v1/tenants/{id}/usage`, which also lists its largest stores and queues).
- `tw doctor` checks the free space on the state dir's volume (`data_disk`) and
  warns when a store, queue or tenant quota is 80% full (`store_sizes`).
- Audit retention, off by default: `tw up --audit-max-age 2160h` or
  `--audit-max-records N` (`tw-mgmt -audit-max-age`, `-audit-max-records`)
  deletes each chain's oldest records and keeps a checkpoint, so what remains
  still verifies (`prunedThrough` in `GET /v1/audit/verification`). Each prune
  is audited (`audit_pruned`).
- Request records, off by default: `tw up --request-log-max-age 168h`
  (`tw-hostd -request-log-max-age`) drops request records older than that.
- `tw up`'s `mgmt.log` and `hostd.log` now rotate at 32 MiB (one previous file kept).

Operator action: none to upgrade. Two new migrations add three `tenants`
columns and the `audit_checkpoints` table. The audit table's append-only
guard now lets the pruner delete records its checkpoint covers; while an
older tw-mgmt replica is still running against the same Postgres store, its
start reinstalls the older guard and pruning waits for the next pass after
the upgrade finishes.

### Fixes

#### deflake-196-lock-renew

The lock template's client (`templates/lock/src/client.ts`): `withLock` now
throws `lease_expired` when the lease's deadline passed before your function
returned, even if its expiry timer hadn't fired yet (a busy event loop). It
used to return normally, though the work may have outlived the lease. Apps
that copied `client.ts` from an earlier template can copy it again.

#### deflake-210-cli-e2e

`tw deploy` no longer ends in `identity_pending` ("not ready after 3m0s
(status active, node minted)") when it runs while a freshly started host is
still bringing up the previous deployment of the same workload, for example
right after `tw up`. The host's node joined, but its ready report still
carried the old lease epoch, and mgmt refused it with `lease_lost`. The host
now refreshes the epoch from mgmt and sends the report again.

#### fix-200-cached-rejoin

A node that the coordination server rejects after it came up is now
re-minted under the same hostname instead of staying "ready" on a dead
device. This happened after a host restart when the node had a saved
netmap cache (it comes up from the cache before control answers), and when
a running node's key expired or its device was removed. The host closes the
dead node, joins a new one with a fresh key, and reports the new node to
mgmt. A node that is being killed or stopped is not brought back.

#### perf-245-real

After a reboot, tw is healthy again about 1 s after its service starts.
It used to take about 30 s: the host agent's first request to mgmt left
before the tailnet knew the route, and it waited out TCP's retransmits.
`systemctl --user restart tw` no longer sometimes takes 5 s extra. A
tailnet node could wait out a 5 s grace on close for a local connection
nobody was using. On a Linux VM against production control, restart to
healthy went from 8.0 s to 3.0 s (two-process layout) or 1.4 s
(single-process), and reboot to healthy went from 38 s to 7.3–7.7 s.

#### perf-245-up-down

`tw down` and `systemctl --user restart tw` no longer take 5 s extra: mgmt
stopped its node while the HTTPS certificate prefetch was still waiting on
the node, and the node waited out a 5 s grace for it. `tw up` no longer
waits for its first network check and a relay (DERP) connection before
mgmt serves: it is ready once mgmt's node is logged in. On a Linux VM,
`tw up --service` went from 3.5 s to 0.5 s and `tw down --service` from
5.1 s to 0.25 s. tw also keeps serving the local CLI right after a restart
while the relay is unreachable, for example during a boot before the
network is up. On `tw down`, mgmt revokes its unused warm keys 8 at a time
instead of one by one.

#### perf-tw-up-keyfile

`tw up` is faster: it no longer joins mgmt's tailnet node itself and then
closes it for tw-mgmt to open again. Each node start costs about a second
of round trips to Tailscale's control plane, and `tw up` now does one
(two with `--separate-host`) instead of two (three). On a Linux VM against
production control, `tw up --service` to ready went from 3.8 s to
about 2.5 s (single process) and from 5.4 s to 4.2–4.5 s
(`--separate-host`). On macOS, `tw up` went from about 3.1–3.5 s to about
1.8–2.0 s.

On the first start, `tw up` now hands mgmt its single-use join key in
`<state>/mgmt/join.env` (0600, in mgmt's 0700 state dir) instead of using
it in-process. mgmt deletes the file once it has read the key; a failed
start deletes it too, and so does `tw up` when it returns. The key is
pre-authorized, tagged `tag:tw-mgmt` and expires within 5 minutes. A
host's own join file already worked this way. If mgmt's join fails, `tw
up` reports control's reason and stops the supervisor (on a first
`tw up --service`, it also removes the service).

#### test-113-linux-matrix

Idle pre-joined pool nodes (ADR-031) now rejoin from saved state when
tailworks restarts or the host reboots. Before, they stayed offline while
mgmt still listed them as ready, so the first create of their class after a
restart registered a new node instead of using the warm one, and the old
device was left behind.

## v0.3.0 (2026-11-21)

workerd 1.20261118.1.

### Security

#### deps-112-hygiene

tw-mgmt's API request validation uses kin-openapi v0.149.0 (was v0.142.0),
which fixes eight advisories that govulncheck found reachable from tw-mgmt,
among them unauthenticated panics on a malformed request
(GO-2026-6783, GO-2026-6112). How tailworks tracks and ships
fixes for advisories in what it bundles (Go modules, workerd, Tailscale,
Firecracker, the guest kernel) is now written down in
docs/RELEASING.md, "Upstream advisories".

#### docs-118-security-refresh

The security docs match the code again. `security/THREAT-MODEL.md` describes the
process-tier sandboxes as they are (Seatbelt on macOS; Landlock and seccomp on
Linux, which confine TCP only with Landlock ABI 4 or later) and adds the
boundaries for `/mcp`, public ingress, upgrades and the hosts' push API.
`docs/security-model.md` lists the `/mcp` and release-signature boundaries. Every
open item in `security/LEDGER.md` now has a GitHub issue with the `security`
label, and every claim the ledger marks pinned names a test that drives the
code tailworks runs. No behaviour changed.

#### fix-121-seatbelt

macOS: the process-tier Seatbelt profile is now strict. A workerd or Node
app can write only its own dirs, open no network except its own unix
sockets and the TCP ports hostd gives it (a Node app: its egress proxy and
its own port), signal only itself and its children (not tw-hostd, tw-mgmt or
other workloads), and exec only its runtime, its own dirs and the OS dirs.
Node-tier egress on macOS is now reported as `enforced` in `tw status`, and
`tw doctor`'s sandbox check also probes the write and signal denials.

Operator action: none. If the strict profile breaks a workload, start
tw-hostd with `-sandbox seatbelt-legacy` (the previous files-only profile;
kept for one release) and report it.

#### fix-149-uffd-validate

Linux microVM hosts: `tw-vmhelper` now validates the guest memory region table that Firecracker sends when a VM is restored from a snapshot (page size, alignment, sizes, overflow, bounds against the snapshot memory file, overlap). A malformed table, which only a tenant that had already escaped its guest into Firecracker could send, used to crash the root helper and drop every VM on the host; now it fails that one VM's start. The helper also recovers from a panic in any one connection or VM session instead of exiting.

#### fix-150-terminal-escapes

`tw` no longer prints tenant- or internet-controlled text raw. Before, a
public request path, an object name, a tenant's audit note or a line of
console output could carry terminal escape sequences, CR/LF or bidi
overrides. These could recolour or clear the operator's terminal, or forge
rows in `tw audit`. Human-readable output (`tw ls`, `tw audit`, `tw logs`,
`tw kill`, `tw deployments`, `tw previews`, `tw approvals`, the KV, R2,
queue and D1 listings, `tw status`, `tw host ls` and error messages) now
shows control characters escaped, as `\x1b`, `\r`, `\n`, `\u202e` and so
on. `--json` output decodes to the same values as before. DEL, C1
controls and bidi overrides, which encoding/json leaves raw, are now sent
as `\u` escapes.

#### fix-152-cron-overlap

Cron triggers: a `scheduled()` run now lasts until the promises it passes to
`ctx.waitUntil()` settle, as on Cloudflare, and is cancelled at the 15-minute
limit. Before, work left in `waitUntil` kept running after the run was
reported done, so the trigger's next run could start beside it. A timed-out
run is logged as `cron trigger timed out` (`outcome=exceededTimeout`).

#### fix-153-admit-cache

A killed object can no longer be brought back with a new tailnet node by
code that was allowed to create it before the kill. A kill now ends every
permission to create that name: the host forgets any pending create
admission and any public-create record for it, and refuses new admissions
for the name (`410 killed`) until it is purged. A tenant's deletion forgets
all of the tenant's admissions. The runtime's per-isolate admit cache keeps
only "this object exists" answers, for at most 60 seconds, and a plain
Durable Object's kill clears its entry.

#### fix-94-kill-window

`tw kill` no longer returns while the killed object can still answer through
its workload. When a host was already killing the object (the heartbeat had
picked up the kill), mgmt's kill push used to return at once, before workerd
had stopped the object, so a request through the workload could still reach
it for a moment. The push now waits for that kill to finish. If workerd does
not confirm the in-process kill, the host now reports the push as failed
instead of succeeded, and the next heartbeat tries again.

#### go-1.27.4

Binaries are now built with Go 1.27.4, which fixes GO-2026-6859 (a header
race in `net/http`'s `MaxBytesReader`). Building from source needs Go 1.27.4
or later.

### Upgrade actions

#### deps-workerd-1.20261118.1

The pinned workerd moves from 1.20261114.1 to 1.20261118.1. That fixes two
memory-safety issues in workerd's Rust JSG bindings (cloudflare/workerd#8401,
##8403) and several JSG garbage-collection issues, and fixes nested dynamic
imports (#8517, #8541). A `DigestStream` now stays alive while its sink runs
(#8533), and `CompressionStream` and `DecompressionStream` gain `"brotli"`
(#8483). Apps don't need changes.

Operator action: workerd moves to 1.20261118.1.

#### e2e-111-upgrade

`tw up` on a state dir that is already set up now keeps the settings it
was set up with (host ID, hostname prefix, control and API URLs,
`--ephemeral`, `--allow-sibling-egress`, `--local-dev`, warm keys, pool
nodes, heartbeat, …) unless you pass the flag again. Each flag left out
used to fall back to its default and was saved. `tw upgrade` restarts a
`tw up` that doesn't run as a service with `tw up --yes`, so after an
upgrade, or the rollback of a failed one, tailworks came back with those
defaults instead (another host ID, for example), and on a `--local-dev`
state the restart failed. `tw upgrade --plan` on a machine where tw isn't
running no longer lists a rollback step: there is no health check then.

Operator action: when upgrading **from v0.2.0**, a rollback restarts
v0.2.0's own `tw up`, which still has this bug. If an upgrade rolls back,
run `tw status` afterwards and, if needed, run `tw up` again with the flags
you originally set it up with (for example `--host-id`,
`--hostname-prefix`, `--ephemeral`). Installs that run as a service
(`tw up --service`) aren't affected.

#### fix-107-tenant-purge

`tw tenant rm` now deletes everything the tenant owns. Before, it left the
tenant's KV, D1 and R2 journals, queue messages, workload secrets, store
and queue data keys, volume keys, notification and telemetry settings and
its files on hosts behind, where no API could reach them. Now mgmt deletes
the tenant's custody entries (the crypto-shred), then every row of the
tenant in one transaction, and every host removes `tenants/<id>/` and the
tenant's R2 blobs at its next heartbeat (audited `tenant_purge_host`). The
`tenant_delete` audit record counts what was removed. If a delete fails part
way, run `tw tenant rm` again to finish it. Deleting a tenant no longer
releases the leases of another tenant whose ID matches it as a `LIKE`
pattern (`_`).

Operator action: hosts with an S3 R2 backend log the prefix
(`t/<tenant>/`) to delete by hand; mgmt cannot list that bucket. Custody
entries written before this release by workloads that were never deployed
(secrets only) are not indexed, so a tenant deleted later can leave them.

#### fix-121-seatbelt

macOS: the process-tier Seatbelt profile is now strict. A workerd or Node
app can write only its own dirs, open no network except its own unix
sockets and the TCP ports hostd gives it (a Node app: its egress proxy and
its own port), signal only itself and its children (not tw-hostd, tw-mgmt or
other workloads), and exec only its runtime, its own dirs and the OS dirs.
Node-tier egress on macOS is now reported as `enforced` in `tw status`, and
`tw doctor`'s sandbox check also probes the write and signal denials.

Operator action: none. If the strict profile breaks a workload, start
tw-hostd with `-sandbox seatbelt-legacy` (the previous files-only profile;
kept for one release) and report it.

### Features

#### fix-147-degraded-stores

mgmt now knows when a platform store is degraded (its object on a host stopped its replay at a journal entry it can't apply, `store_degraded`). The host reports it when it happens and when it heals; mgmt records it on the store (`degraded` on the stores API: the entry's seq, the error, the host and when), audits it (`store_replay_failed`), and shows it in `tw d1 list`, `tw kv namespace ls`, `tw r2 bucket ls`, `tw status` (a new `GET /v1/stores/degraded`) and the console's overview. New: `tw d1 repair <db> --skip <seq> --yes` (`POST …/stores/{kind}/{name}/repair`) deletes the one entry the database is degraded at from its journal, so every host's replay goes on to the next one and the writes after it are kept; it is audited (`store_repair`) and refused with the new error `store_not_degraded` for any other entry. docs/d1.md "Recovery" covers both ways out (skip, or rebuild from an export).

Operator action: none. The migration adds five columns to `stores`. A new tw-hostd against an older tw-mgmt logs a warning when a store is degraded (the report has nowhere to go) and otherwise works as before.

### Fixes

#### deflake-140

A host that self-fences (it lost its heartbeat to mgmt) now stops claiming
queue messages at once. Before, a consumer's long poll that was already
waiting could still claim a message sent in the first half second or so
after the fence. The host handed it back, since its workload was stopped,
but a fenced host must not claim anything.

#### deflake-182

A host restart no longer stalls an object for 2 minutes when control has expired the object's node key and its saved state is still marked logged in. That happens to a persistent object, or to an ephemeral one whose logout at shutdown didn't complete. hostd now detects the rejection within about 2 s and joins again under the same hostname with a fresh key. Before, the rejoin waited out the join timeout and held a join slot for the whole time.

#### deps-workerd-1.20261118.1

The pinned workerd moves from 1.20261114.1 to 1.20261118.1. That fixes two
memory-safety issues in workerd's Rust JSG bindings (cloudflare/workerd#8401,
##8403) and several JSG garbage-collection issues, and fixes nested dynamic
imports (#8517, #8541). A `DigestStream` now stays alive while its sink runs
(#8533), and `CompressionStream` and `DecompressionStream` gain `"brotli"`
(#8483). Apps don't need changes.

Operator action: workerd moves to 1.20261118.1.

#### e2e-111-upgrade

`tw up` on a state dir that is already set up now keeps the settings it
was set up with (host ID, hostname prefix, control and API URLs,
`--ephemeral`, `--allow-sibling-egress`, `--local-dev`, warm keys, pool
nodes, heartbeat, …) unless you pass the flag again. Each flag left out
used to fall back to its default and was saved. `tw upgrade` restarts a
`tw up` that doesn't run as a service with `tw up --yes`, so after an
upgrade, or the rollback of a failed one, tailworks came back with those
defaults instead (another host ID, for example), and on a `--local-dev`
state the restart failed. `tw upgrade --plan` on a machine where tw isn't
running no longer lists a rollback step: there is no health check then.

Operator action: when upgrading **from v0.2.0**, a rollback restarts
v0.2.0's own `tw up`, which still has this bug. If an upgrade rolls back,
run `tw status` afterwards and, if needed, run `tw up` again with the flags
you originally set it up with (for example `--host-id`,
`--hostname-prefix`, `--ephemeral`). Installs that run as a service
(`tw up --service`) aren't affected.

#### fix-106-d1-replay

A platform store (D1 in particular) no longer skips a journal entry it can't apply when it catches up or rebuilds from the journal, and no longer reports itself synced past it. It stops before the entry and is degraded: reads answer from what it applied, writes fail with the new error code `store_degraded` (`409` from the mgmt API, `D1_ERROR: [store_degraded] …` in a Worker), and it tries the entry again on later syncs. `tw d1 export` of a degraded database exports what applied, headed by a comment naming the failing entry; docs/d1.md "Recovery" has the steps to rebuild it.

#### fix-107-tenant-purge

`tw tenant rm` now deletes everything the tenant owns. Before, it left the
tenant's KV, D1 and R2 journals, queue messages, workload secrets, store
and queue data keys, volume keys, notification and telemetry settings and
its files on hosts behind, where no API could reach them. Now mgmt deletes
the tenant's custody entries (the crypto-shred), then every row of the
tenant in one transaction, and every host removes `tenants/<id>/` and the
tenant's R2 blobs at its next heartbeat (audited `tenant_purge_host`). The
`tenant_delete` audit record counts what was removed. If a delete fails part
way, run `tw tenant rm` again to finish it. Deleting a tenant no longer
releases the leases of another tenant whose ID matches it as a `LIKE`
pattern (`_`).

Operator action: hosts with an S3 R2 backend log the prefix
(`t/<tenant>/`) to delete by hand; mgmt cannot list that bucket. Custody
entries written before this release by workloads that were never deployed
(secrets only) are not indexed, so a tenant deleted later can leave them.

#### fix-110-supervise-exit

The supervisor (`tw __supervise`) now exits 1 when mgmt or the host stops
on its own: mgmt's listener failed after start (single process), or a
tw-mgmt or tw-hostd child kept exiting and the supervisor gave up (two
processes). It used to exit 0, which systemd (`Restart=on-failure`) and
launchd (`KeepAlive` on an unsuccessful exit) treat as a clean stop, so a
`tw up --service` install stayed down. A clean stop (`tw down`,
`systemctl --user stop`) still exits 0. The exit error (`mgmt_failed` or
`host_failed`) is in `tw logs --component supervisor`.

#### fix-117-worker-egress

A Worker's `fetch()` to an internet name (for example `https://api.github.com/`) is now refused with 403 `internet_egress_unavailable` and a one-line detail that links the gateway recipe (docs/recipes.md "Call an internet API"), instead of `peer_not_visible` with a hint about grants that could never help. A tailnet name that isn't in the netmap (a bare short name, a name under a tailnet's MagicDNS suffix, or any `*.ts.net` name) is still `peer_not_visible`. Nothing that was refused is now allowed. The README and docs now say plainly that Workers have no internet egress and that no setting turns it on; `tailnet.egress.internet` applies to containers only. Code that matched `peer_not_visible` for internet URLs should match `internet_egress_unavailable` as well.

## v0.2.0 (2026-11-17)

workerd 1.20261114.1.

### Breaking

#### delete-confirm-issue-72

Every command that deletes data or a tenant's resources now asks first:
`tw d1 delete`, `tw kv namespace delete`, `tw queues rm`,
`tw queues purge`, `tw r2 bucket rm`, `tw tenant rm` and `tw host rm`.
On a terminal it says what will be lost and asks you to type the name.
Without a terminal (a script, a coding agent, `--json`) it refuses with
`confirmation_required` and does nothing; add `--yes` to go ahead.
`tw d1 delete` without `--yes` now prompts on a terminal, and its refusal
code is `confirmation_required` (it was `bad_request`).

Operator action: scripts that run these commands need `--yes`.

#### observability-logs-metrics-traces-20

`tw logs <workload>` now prints one structured line per request, with filters (`--errors`, `--status`, `--trace`, …) and `--json` request records. Pass `--console` for the app's raw output, which used to be the default. tailworks also gains Prometheus `/metrics` and OTLP export of logs, metrics and traces (`tw telemetry`).

Operator action: scripts that read `tw logs` output need `--console`. tw-hostd and tw-mgmt now serve `/metrics` on port 9464 of their tailnet node by default (`-metrics-port 0` turns it off); scraping it needs a policy grant.

#### remove-wif-apikey

tailworks now mints node keys on Tailscale with an OAuth client only. The
`apikey` and `wif` (workload identity federation) mint modes are removed
(ADR-042). The Headscale profile is unchanged: it still
mints with a Headscale API key.

Removed: `tw up --wif`, `--wif-client-id`, `--wif-lifecycle-client-id`,
`--issuer-authkey-file`, `--login-timeout`, and `--api-key-file` outside
`--control=headscale`; `tw tenant set --wif-client-id` and
`--wif-lifecycle-client-id`; `tw doctor --tailnet/--api-key-file`; mgmt's
OIDC issuer (`tw-mgmt -issuer-url`, `-issuer-listen`, `-funnel`,
`-issuer-tsnet-*`, and the discovery and JWKS endpoints);
`POST /v1/tenants/{id}/wif-check`; the tenant fields `wifClientID`,
`wifLifecycleClientID` and `wif`; `tw-netd -mint-api-key-file` (now
`-mint-oauth-client-file`). New: `tw tenant add --oauth-client-file`,
`tw tenant set --oauth-client-file`, and the error code
`mint_mode_removed`.

Operator action: if `tw doctor` fails its `mint_mode` row, or `tw up`
says `mint_mode_removed`, the install mints with an API key or WIF. Create
an OAuth client (docs/INSTALL.md, "The OAuth client"), then run
`tw up --oauth-client-file <file>`. This keeps the tenant, its deployments
and mgmt's node. Do the same for each added tenant with
`tw tenant set <tenant> --oauth-client-file <file>`. Then revoke the old
API key, or delete the WIF trust credentials, the `tw-issuer` machine and
the `tag:tw-issuer` policy entries. Running nodes keep serving until then,
but nothing new can be minted.

#### rename-twd-to-tailworks-86

twd is now **tailworks** (issue #86). The project, the repo
(`github.com/rosskukulinski/tailworks`), the Go module, the npm packages
(`@tailworks/tailnet`, `@tailworks/node`, `@tailworks/mcp`,
`npm create tailworks`), the install root (`~/.tailworks`), the per-app
config (`tailworks.jsonc`, top-level key `"tw"`) and the release tarballs
(`tailworks-<version>-<os>-<arch>.tar.gz`) use the long name. The command is
`tw` (`install.sh` also links `tailworks` → `tw`), and everything else uses
the short name: the daemons (`tw-mgmt`, `tw-hostd`, `tw-netd`,
`tw-vmhelper`, `tw-vz`, `tw-guestd`), env vars (`TW_*`), tags (`tag:tw`,
`tag:tw-mgmt`, `tag:tw-host`, `tag:tw-public`, `tag:tw-wl-*`), the Service
`svc:tw-mgmt`, node hostnames (`tw-`), headers (`x-tw-*`, `Tw-Error`,
`Tw-Actor`, `Tw-MCP-Tools`), metrics (`tw_*`), the launchd label
`dev.tailworks.up` and the systemd units `tw.service` and
`tw-vmhelper.service`. Peer capabilities move from `twd.dev/cap/*` to
`github.com/rosskukulinski/tailworks/cap/*`.

There is no upgrade path from twd 0.x: nothing reads `~/.twd`,
`twd.jsonc`, the old env vars, tags or headers.

Operator action: tear down and reinstall. On each machine, run
`twd down --purge --service`, remove any hand-made agents
(`dev.twd.hostd`, `twd-vmhelper.service`) and delete `~/.twd`. In the
admin console, remove the `tag:twd*` devices, then the `tag:twd*` tag
owners, `svc:twd-mgmt` and the `twd.dev/cap/*` grants; revoke the OAuth
client and create one scoped to `tag:tw`. Rename each app's `twd.jsonc` to
`tailworks.jsonc` (key `"tw"`) and its `@twd/*` imports to
`@tailworks/*`. Then install with `get.sh` and run `tw doctor`.

### Security

#### audit-chain-flood-issue-68

The host audit chain survives a flood (red-team H03, #68). Before, a flood
of tenant `audit()` notes or of public requests to unlisted paths, while
mgmt was slow or unreachable, made hostd drop the oldest queued records.
Those records were already chained, so mgmt refused every later batch and
the host's chain stayed broken for good. Now hostd never drops a chained
record: host records spill to `<data-dir>/audit-spill.jsonl` once 10 000
wait in memory, and are flushed in order, in batches. Records that may be
shed are refused before they are chained and counted in a new in-chain
`audit_dropped` record, so the chain still verifies and the gap shows in
`tw audit`. Those records are tenant notes over a per-object rate (10/s,
burst 200) or sent while the queue is backed up, and `public_request`
records once the spill reaches 256 MiB. Public `404 not_public` and
`400 bad_request` refusals are no longer one `public_request` record each.
They are counted in the per-minute `public_rejected` summary
(`notPublic`, `badRequest`).

`audit_dropped` is a new `AuditKind`. An older mgmt stores it like any host
record.

#### explain-tailnet-multi-tenancy-team-workloads

Several teams on one tailnet (ADR-043, docs/teams.md).

- Tenant admins: grant a team `github.com/rosskukulinski/tailworks/cap/tenant-admin` on `tag:tw-mgmt`
  with values like `{"tenants": ["tn_example-pay"], "actions": ["read",
  "deploy", "kill"]}`. The actions are `read`, `deploy`, `kill`, `secrets`
  and `data`.
  - The team can `tw link`, deploy, list, read logs and audit, and kill in
    those tenants, through the CLI, the API, the registry and the admin
    console.
  - An `"mcp"` entry admits tailworks's MCP server for those tenants, and an
    `"approve"` entry makes the team an approver there only.
  - Hosts, tenant settings, releases and upgrades stay with admins.
  - Refusals are `403 tenant_forbidden` with the grant to add; another
    tenant's deployment or object is `404`.
- Owner tags and name prefixes: `tw tenant add <tailnet> --owner-tag
  tag:tw-t-<team> --name-prefix <p>` adds a second team's tenant to a
  tailnet. Each tenant on a shared tailnet must have its own owner tag
  (`409 shared_owner_tag`), and the OAuth client scoped to it can't mint
  another team's tags. Its tailnet names get the prefix (`<p>-…`,
  `svc:<p>-…`).
- `tw policy` and `tw deploy` warn about grants to `tag:tw-mgmt` that
  don't do what they look like (`accessWarnings`), and about tags another
  team owns (`tagOwnerProblems`).

Fix: a tailnet user holding only `github.com/rosskukulinski/tailworks/cap/approve` could push and
pull every tenant's images in mgmt's registry. The registry now admits only
admins, hosts and tenant admins.

Operator action: none for a tenant alone on its tailnet. To add a second
team's tenant to a tailnet, first move the existing tenant to its own owner
tag (`tw tenant set <tenant> --owner-tag … --oauth-client-file …`). Don't
scope a team by adding values to `github.com/rosskukulinski/tailworks/cap/admin`: older mgmts ignore
them and treat the holder as a full admin.

#### prelaunch-100-security-model

`tw status` and `GET /v1/status` no longer report Node-tier egress as `enforced` on a Linux host whose Landlock ABI is below 4 (kernels before 6.7, such as Ubuntu 22.04's 5.15). Landlock can't restrict TCP there, so egress is now reported as `unenforced`, as on macOS. netd now strips client copies of every `Tailscale-*` header and the underscore spellings `X_TW_*` and `tw_*` by prefix, not only a list of exact names. docs/security-model.md now lists what the process-tier sandbox confines on each platform and Landlock ABI, which hosts may be shared between tenants, and whom a tenant trusts (host operator, mgmt operator, tailnet admin) (#100).

#### prelaunch-105-host-allowlist

tw-mgmt now refuses tailnet requests whose `Host` header is not one of its own names (its MagicDNS FQDN or short name, `-tsnet-hostname`, `-tsnet-service`) or an IP literal, with the new error `421 misdirected_request`. This closes a DNS-rebinding hole: before, a web page on an attacker's domain rebound to mgmt's tailnet IP was same-origin with mgmt's plain-HTTP `:80` listener and could read `/v1` and post admin UI forms with the admin's identity. The `tw` CLI, hosts and the admin UI address mgmt by those names or its IP and are unaffected. The admin UI's `Origin` check is now tied to the same allowlist.

Operator action: if you reach tw-mgmt through a CNAME or another DNS name of your own, use its tailnet name or IP instead. If you renamed the mgmt node, restart tw-mgmt so it learns the new name.

#### prelaunch-98-upgrade-sig

`tw upgrade` on a `tw up` machine now checks the release signature. It extracts the tarball with the hardened extractor tw-hostd uses, then checks its `SHA256SUMS.sig` against the release keys built into `tw` (or `TW_RELEASE_SIGNERS`) with the same verifier as tw-hostd's self-upgrade, before anything in the release runs. Before, it only compared the tarball with a `SHA256SUMS` downloaded from the same place, and `--from <dir>` checked nothing, so a tampered tarball's own `install.sh` would have run. An unsigned release is refused with `release_unsigned` and a badly signed one with `release_bad_signature`, on `--plan` too and for `--from`. `tw upgrade --allow-unsigned` (or `TW_ALLOW_UNSIGNED=1`) installs a build you made yourself, with a warning in the plan. `tw upgrade <version>` also refuses a download whose release is a different version (#98).

#### public-conn-limit-issue-69

One client can no longer fill a workload's public (Funnel) connections and lock everyone else out. The public listener now caps connections per client address (`public.limits.maxConnsPerIp`, default 32; an IPv6 client counts per /64), gives a new connection 5 seconds to send its first request, and closes upgraded (WebSocket) connections after `public.limits.upgradeLifetimeSeconds` (default 3600). Refused connections are counted in the per-minute `public_rejected` audit summary (`ipConnsRefused`) and in the new `tw.public.rejected` metric (#69).

Operator action: WebSocket clients of a public workload must reconnect when the lifetime ends; raise `upgradeLifetimeSeconds` or `maxConnsPerIp` in `tailworks.jsonc` if your users need longer sessions or share one address. A host on this version keeps the new defaults even while mgmt is older.

#### public-name-claim-issue-70

Public ingress: a public name now has one owner per tailnet (red-team
H10). The first tenant to deploy `"public"` routes under a workload name
owns `https://<name>.<tailnet>.ts.net/` on that tailnet, across undeploys,
redeploys and kills, until it releases the name. Another tenant on the same
tailnet that declares `"public"` under that name is refused with
`409 public_name_owned`, at deploy, at `--dry-run`, and when its host asks
for the public node. Before this, a second tenant could take the name after
a kill and redeploy and receive the first tenant's public traffic, such as
signed webhooks.

New API: `GET /v1/tenants/{id}/public-names` lists a tenant's names, and
`DELETE /v1/tenants/{id}/public-names/{name}` releases one once nothing
serves it (`409 public_name_in_use` otherwise). Deleting a tenant releases
its names. New audit kinds: `public_name_claim`, `public_name_release`.

Operator action: none. The store migration adds the `public_names` table.
Names already served before the upgrade are claimed for the tenant serving
them the first time anyone deploys or recreates them, so the upgrade can't
hand a name to another tenant. If several tenants share a tailnet, give
them all the same `tailnet` name (its MagicDNS suffix), because ownership is
scoped by that name.

#### release-signing-issue-71

Releases are signed. Each release's `SHA256SUMS`, and each tarball's, gets a `SHA256SUMS.sig`: an SSH signature (`ssh-keygen -Y`) by a tailworks release key that is pinned in `scripts/get.sh`, `install.sh` and the tailworks binaries. `get.sh` checks the signature before it trusts the checksums, and `install.sh` checks it before it installs. tw-hostd's self-upgrade checks it in Go against the keys built into the running binary, so a compromised tw-mgmt can no longer push a release of its own. An unsigned or badly signed release is refused. `install.sh --allow-unsigned` (or `TW_ALLOW_UNSIGNED=1`) installs a build you made yourself, and local builds (`x.y.z-<commit>`) install with a warning. `get.sh` refuses an unsigned release whatever its tag (no unsigned release was ever published). tw-hostd also checks that a release it is ordered to install is that version (`VERSION`), and extracts tarballs so that no entry can write or link outside the extract directory. To check a release by hand, see INSTALL.md, "Verify a release" (#71).

Operator action: a host that self-upgrades now refuses unsigned releases. Until a release key is pinned (docs/RELEASING.md, "Signing"), upgrade hosts by hand.

#### review-fixes

Security fixes from review. `tw up` runs as a single process by default (mgmt with the host agent embedded; `--separate-host` keeps the old layout), and tw-mgmt serves an admin UI at `/ui/`.

### Upgrade actions

#### benchmark-dx-infra-vs-cloudflare-workerd-vercel

`tw dev` prints each request with its `console.*` lines and the stack of any uncaught exception (`--json`: a new `request` event), and picks up edits within about 0.2 s. An unknown command suggests the nearest one (`tw tail` → `tw logs`), `--help` shows each flag's type, and `tw ls` shows why a deployment failed. `tw init` names `package.json` after the app and adds `npm run dev`. The admin console and `tw audit` render audit records the same way (no raw JSON); the approvals page has one comment box with Approve and Reject buttons. `tw docs search` ranks the guides above the API contract and the ADRs.

#### cli-ergonomics-issue-46

`tw completion bash|zsh|fish` prints a tab-completion script for every command, subcommand (`kv key put`, `d1 migrations apply`) and flag, generated from the CLI's own command table. `tw whoami [--json]` shows who the CLI acts as (your tailnet login through `tw link`, or the local operator), the mgmt it talks to, the tenant, and what that identity may do: admin, approve and the MCP tools. `GET /v1/whoami` adds `caps`, `approver` and `mcpTools` for it. `tw help` groups the commands (Set up · Develop · Deploy · Data · Observe · Operate) with one short line each; `tw help --all` prints every usage line. `tw version --check` compares with the latest GitHub release, prints `tw upgrade`, and says whether the running `tw up` needs a restart; an interactive `tw` prints a newer-release notice at most once a day (off with `--json`, `CI`, or `TW_NO_UPDATE_CHECK=1`). From a source checkout, `tw doctor` and a `build_failed` for an unresolved `@tailworks/*` import point at `make deps`.

#### delete-confirm-issue-72

Every command that deletes data or a tenant's resources now asks first:
`tw d1 delete`, `tw kv namespace delete`, `tw queues rm`,
`tw queues purge`, `tw r2 bucket rm`, `tw tenant rm` and `tw host rm`.
On a terminal it says what will be lost and asks you to type the name.
Without a terminal (a script, a coding agent, `--json`) it refuses with
`confirmation_required` and does nothing; add `--yes` to go ahead.
`tw d1 delete` without `--yes` now prompts on a terminal, and its refusal
code is `confirmation_required` (it was `bad_request`).

Operator action: scripts that run these commands need `--yes`.

#### deploy-history-rollback-issue-45

`tw deployments <workload>` lists a workload's deployment history, newest
first: id, created, who deployed, bundle SHA-256, status, error and host.
`tw rollback <workload> [<deployment>]` redeploys an earlier deployment's
stored bundle as a new deployment, through the same path as `tw deploy`
(placement, lease epoch, host fencing, preflight); with no ID it goes back
to the previous deployment that ran. It is audited as `kind: rollback`. A
rollback changes code and configuration only: Durable Object storage, D1
(migrations are forward-only), KV, R2 and queues keep their data, and
secrets are the workload's current ones (ADR-046).

mgmt now keeps the bundles of each workload's newest 10 deployments and
prunes older ones, keeping their record and hash (tw-mgmt
`-bundle-retention`, `-1` keeps every bundle). New API:
`GET /v1/tenants/{id}/workloads/{workload}/deployments` and
`POST /v1/tenants/{id}/workloads/{workload}/rollbacks`.

Operator action: none. The store migration adds columns to `deployments`.
The first deploy of each workload after the upgrade prunes that workload's
bundles beyond the newest 10; pass `-bundle-retention -1` to tw-mgmt
beforehand to keep them all.

#### observability-logs-metrics-traces-20

`tw logs <workload>` now prints one structured line per request, with filters (`--errors`, `--status`, `--trace`, …) and `--json` request records. Pass `--console` for the app's raw output, which used to be the default. tailworks also gains Prometheus `/metrics` and OTLP export of logs, metrics and traces (`tw telemetry`).

Operator action: scripts that read `tw logs` output need `--console`. tw-hostd and tw-mgmt now serve `/metrics` on port 9464 of their tailnet node by default (`-metrics-port 0` turns it off); scraping it needs a policy grant.

#### prelaunch-104-update-check

`tw init` and `tw dev` no longer run the once-a-day update check, so they make no outbound request (before, they could ask `api.github.com` for the latest release after they finished). `tw version --json` adds `updateCheck`: whether this tw runs the check (a release build without `CI` or `TW_NO_UPDATE_CHECK` set). The README ("What tw sends") and `docs/security-model.md` ("Telemetry and the update check") now say what the check sends, when it runs and how to turn it off; tailworks has no other telemetry.

#### prelaunch-105-host-allowlist

tw-mgmt now refuses tailnet requests whose `Host` header is not one of its own names (its MagicDNS FQDN or short name, `-tsnet-hostname`, `-tsnet-service`) or an IP literal, with the new error `421 misdirected_request`. This closes a DNS-rebinding hole: before, a web page on an attacker's domain rebound to mgmt's tailnet IP was same-origin with mgmt's plain-HTTP `:80` listener and could read `/v1` and post admin UI forms with the admin's identity. The `tw` CLI, hosts and the admin UI address mgmt by those names or its IP and are unaffected. The admin UI's `Origin` check is now tied to the same allowlist.

Operator action: if you reach tw-mgmt through a CNAME or another DNS name of your own, use its tailnet name or IP instead. If you renamed the mgmt node, restart tw-mgmt so it learns the new name.

#### public-conn-limit-issue-69

One client can no longer fill a workload's public (Funnel) connections and lock everyone else out. The public listener now caps connections per client address (`public.limits.maxConnsPerIp`, default 32; an IPv6 client counts per /64), gives a new connection 5 seconds to send its first request, and closes upgraded (WebSocket) connections after `public.limits.upgradeLifetimeSeconds` (default 3600). Refused connections are counted in the per-minute `public_rejected` audit summary (`ipConnsRefused`) and in the new `tw.public.rejected` metric (#69).

Operator action: WebSocket clients of a public workload must reconnect when the lifetime ends; raise `upgradeLifetimeSeconds` or `maxConnsPerIp` in `tailworks.jsonc` if your users need longer sessions or share one address. A host on this version keeps the new defaults even while mgmt is older.

#### release-signing-issue-71

Releases are signed. Each release's `SHA256SUMS`, and each tarball's, gets a `SHA256SUMS.sig`: an SSH signature (`ssh-keygen -Y`) by a tailworks release key that is pinned in `scripts/get.sh`, `install.sh` and the tailworks binaries. `get.sh` checks the signature before it trusts the checksums, and `install.sh` checks it before it installs. tw-hostd's self-upgrade checks it in Go against the keys built into the running binary, so a compromised tw-mgmt can no longer push a release of its own. An unsigned or badly signed release is refused. `install.sh --allow-unsigned` (or `TW_ALLOW_UNSIGNED=1`) installs a build you made yourself, and local builds (`x.y.z-<commit>`) install with a warning. `get.sh` refuses an unsigned release whatever its tag (no unsigned release was ever published). tw-hostd also checks that a release it is ordered to install is that version (`VERSION`), and extracts tarballs so that no entry can write or link outside the extract directory. To check a release by hand, see INSTALL.md, "Verify a release" (#71).

Operator action: a host that self-upgrades now refuses unsigned releases. Until a release key is pinned (docs/RELEASING.md, "Signing"), upgrade hosts by hand.

#### remove-wif-apikey

tailworks now mints node keys on Tailscale with an OAuth client only. The
`apikey` and `wif` (workload identity federation) mint modes are removed
(ADR-042). The Headscale profile is unchanged: it still
mints with a Headscale API key.

Removed: `tw up --wif`, `--wif-client-id`, `--wif-lifecycle-client-id`,
`--issuer-authkey-file`, `--login-timeout`, and `--api-key-file` outside
`--control=headscale`; `tw tenant set --wif-client-id` and
`--wif-lifecycle-client-id`; `tw doctor --tailnet/--api-key-file`; mgmt's
OIDC issuer (`tw-mgmt -issuer-url`, `-issuer-listen`, `-funnel`,
`-issuer-tsnet-*`, and the discovery and JWKS endpoints);
`POST /v1/tenants/{id}/wif-check`; the tenant fields `wifClientID`,
`wifLifecycleClientID` and `wif`; `tw-netd -mint-api-key-file` (now
`-mint-oauth-client-file`). New: `tw tenant add --oauth-client-file`,
`tw tenant set --oauth-client-file`, and the error code
`mint_mode_removed`.

Operator action: if `tw doctor` fails its `mint_mode` row, or `tw up`
says `mint_mode_removed`, the install mints with an API key or WIF. Create
an OAuth client (docs/INSTALL.md, "The OAuth client"), then run
`tw up --oauth-client-file <file>`. This keeps the tenant, its deployments
and mgmt's node. Do the same for each added tenant with
`tw tenant set <tenant> --oauth-client-file <file>`. Then revoke the old
API key, or delete the WIF trust credentials, the `tw-issuer` machine and
the `tag:tw-issuer` policy entries. Running nodes keep serving until then,
but nothing new can be minted.

#### rename-twd-to-tailworks-86

twd is now **tailworks** (issue #86). The project, the repo
(`github.com/rosskukulinski/tailworks`), the Go module, the npm packages
(`@tailworks/tailnet`, `@tailworks/node`, `@tailworks/mcp`,
`npm create tailworks`), the install root (`~/.tailworks`), the per-app
config (`tailworks.jsonc`, top-level key `"tw"`) and the release tarballs
(`tailworks-<version>-<os>-<arch>.tar.gz`) use the long name. The command is
`tw` (`install.sh` also links `tailworks` → `tw`), and everything else uses
the short name: the daemons (`tw-mgmt`, `tw-hostd`, `tw-netd`,
`tw-vmhelper`, `tw-vz`, `tw-guestd`), env vars (`TW_*`), tags (`tag:tw`,
`tag:tw-mgmt`, `tag:tw-host`, `tag:tw-public`, `tag:tw-wl-*`), the Service
`svc:tw-mgmt`, node hostnames (`tw-`), headers (`x-tw-*`, `Tw-Error`,
`Tw-Actor`, `Tw-MCP-Tools`), metrics (`tw_*`), the launchd label
`dev.tailworks.up` and the systemd units `tw.service` and
`tw-vmhelper.service`. Peer capabilities move from `twd.dev/cap/*` to
`github.com/rosskukulinski/tailworks/cap/*`.

There is no upgrade path from twd 0.x: nothing reads `~/.twd`,
`twd.jsonc`, the old env vars, tags or headers.

Operator action: tear down and reinstall. On each machine, run
`twd down --purge --service`, remove any hand-made agents
(`dev.twd.hostd`, `twd-vmhelper.service`) and delete `~/.twd`. In the
admin console, remove the `tag:twd*` devices, then the `tag:twd*` tag
owners, `svc:twd-mgmt` and the `twd.dev/cap/*` grants; revoke the OAuth
client and create one scoped to `tag:tw`. Rename each app's `twd.jsonc` to
`tailworks.jsonc` (key `"tw"`) and its `@twd/*` imports to
`@tailworks/*`. Then install with `get.sh` and run `tw doctor`.

#### upgrade-prefix-health-issue-63

`tw upgrade` on a `tw up` machine now replaces the install that runs the state dir (the binary the service or the running supervisor was started from, or the state dir itself), not whichever directory the `tw` binary happens to live in. Running it from an extracted release, as the first upgrade from 0.1.0 needs, used to plan against the extracted directory; it now refuses with `prefix_mismatch` unless `--prefix` names the install. docs/upgrading.md covers the first upgrade.

The health check after the restart now only waits for deployments that were active before the upgrade. One that had already failed no longer rolls a good upgrade back; `--plan` and the start of the run warn about it, and the result lists it (`notActiveBefore` in `--json`) (#63).

#### workerd-restart-issue-43

A workerd that crashes after its deployment has served is now restarted until it comes back. hostd retries with exponential backoff (200 ms doubling to 5 min) and never gives up. Before, six quick exits marked the deployment `failed` for good. Before each restart hostd kills the old workerd's process group and waits for it to empty. The stale ingress socket is then removed, so a restart no longer fails with `bind(): Address already in use`. hostd also no longer misses an exit when a child process still holds workerd's log. From the second exit in a row the deployment reports a new status, `crashloop`, and its error gives the attempt count, the next delay and the last log lines. `tw ls`, `--json`, the console and the API show this status. The deployment goes back to `active` by itself once workerd answers again. `tw deploy` stops waiting with `deploy_crashloop` when the deployment enters a crash loop. A deploy whose workerd never becomes healthy (a bad bundle or config) still ends `failed` within a few seconds.

Operator action: clients that match on deployment status should treat `crashloop` as "assigned here, not serving right now". A deployment left `failed` by this bug on an older host needs one `tw deploy` after the upgrade.

#### workflows-approvals

Cloudflare **Workflows** (wrangler `workflows`) run on workerd's built-in engine, and `step.waitForApproval` is decided by tailnet identities holding `github.com/rosskukulinski/tailworks/cap/approve` (`tw approvals`, `tw approve`; ADR-040).

Operator action: workerd moves to 1.20261114.1.

### Features

#### agent-first-dx-issue-25

Coding agents can drive tailworks: `tw agent setup` installs skills, `--json` output is a stable, documented contract (docs/cli-json.md, `schema/cli.schema.json`), errors carry `hint` and `doc_url`, `tw docs` searches the docs offline, and tw-mgmt serves an MCP server at `/mcp` (ADR-035).

#### app-testing-issue-47

Testing tailworks apps (docs/testing.md). `@tailworks/tailnet/testing` adds
`withCaller` / `withPublic` and a fake `ctx.tailnet` for unit tests, in Node
or under `@cloudflare/vitest-plugin`, which runs tailworks apps' Worker and Durable
Object logic as on Cloudflare. `tw dev --caller <name>[=<tag>,…]` adds extra
dev identities, each with its own local URL, listed as `callers` in the
`ready` event and in `tw dev --once --json`; `@tailworks/tailnet/testing/dev`
(`startDev`) runs an app under `tw dev` for black-box tests. Every template
now has a test and an `npm test` script. `tw dev` no longer misses an edit
made right after its `ready` event.

#### approver-ux-issue-50

Approvers no longer need a tenant ID, and they hear about waiting approvals
(issue #50).

- `GET /v1/whoami` now returns `tailnet` (the caller's own), `tenants` (the
  tenants on that tailnet; every tenant for the local operator) and
  `approveGrants` (the caller's `github.com/rosskukulinski/tailworks/cap/approve` grants). The admin
  UI's approvals page and `tw approvals` / `tw approve` from a linked
  machine default to those tenants; the page hides the tenant box when
  there is exactly one.
- `tw notify set --webhook -` sets a tenant webhook (stored in custody;
  only its origin is ever shown). When a Workflow starts waiting for an
  approval, mgmt POSTs a JSON `approval_requested` event with a deep link
  to the request's row (`/ui/approvals?tenant=…#<instance>`); Slack-style
  incoming webhooks show its `text`. New API: `GET/PUT/DELETE
  /v1/tenants/{id}/notifications`, `POST …/notifications/test`; new audit
  kinds `notification_put`, `notification_delete`, `notification_failed`,
  `notification_test`.
- `GET /v1/audit?order=desc` lists newest first. The console's audit page
  uses it and combines tenant, event, caller, object, trace and app
  filters; a new **Objects** page filters by tenant, app, status, tag and
  acting-for, with paging; every activity list is newest first.
- The approvals page matches the rest of the console: card filter bar,
  facts as a definition list, an anchor per row, and "What you may decide".
- Browsers on public (Funnel) routes get a minimal HTML error page (status,
  code, docs link, nothing about the request) instead of JSON when they ask
  for `text/html`. JSON stays the default.
- `tw dev` serves a read-only data console on `127.0.0.1` (press `e`, or
  open the printed `console on …` URL): Workers KV namespaces, keys and
  values, and D1 tables and rows of the run. `--console-port <port>` picks
  the port (default: a free one); `--console-port -1` turns it off. With
  `--json`, a `console` event gives its URL.

#### benchmark-dx-infra-vs-cloudflare-workerd-vercel

`tw dev` prints each request with its `console.*` lines and the stack of any uncaught exception (`--json`: a new `request` event), and picks up edits within about 0.2 s. An unknown command suggests the nearest one (`tw tail` → `tw logs`), `--help` shows each flag's type, and `tw ls` shows why a deployment failed. `tw init` names `package.json` after the app and adds `npm run dev`. The admin console and `tw audit` render audit records the same way (no raw JSON); the approvals page has one comment box with Approve and Reject buttons. `tw docs search` ranks the guides above the API contract and the ADRs.

#### cli-ergonomics-issue-46

`tw completion bash|zsh|fish` prints a tab-completion script for every command, subcommand (`kv key put`, `d1 migrations apply`) and flag, generated from the CLI's own command table. `tw whoami [--json]` shows who the CLI acts as (your tailnet login through `tw link`, or the local operator), the mgmt it talks to, the tenant, and what that identity may do: admin, approve and the MCP tools. `GET /v1/whoami` adds `caps`, `approver` and `mcpTools` for it. `tw help` groups the commands (Set up · Develop · Deploy · Data · Observe · Operate) with one short line each; `tw help --all` prints every usage line. `tw version --check` compares with the latest GitHub release, prints `tw upgrade`, and says whether the running `tw up` needs a restart; an interactive `tw` prints a newer-release notice at most once a day (off with `--json`, `CI`, or `TW_NO_UPDATE_CHECK=1`). From a source checkout, `tw doctor` and a `build_failed` for an unresolved `@tailworks/*` import point at `make deps`.

#### critique-developer-experience-admin-deployers

Worker secrets (`tw secret`, ADR-034), `tw logs` from a linked machine, and many developer-experience fixes.

#### d1-databases

**D1** databases (wrangler `d1_databases`) with wrangler-layout migrations: `tw d1` creates, queries, exports and migrates them (ADR-038).

#### deploy-history-rollback-issue-45

`tw deployments <workload>` lists a workload's deployment history, newest
first: id, created, who deployed, bundle SHA-256, status, error and host.
`tw rollback <workload> [<deployment>]` redeploys an earlier deployment's
stored bundle as a new deployment, through the same path as `tw deploy`
(placement, lease epoch, host fencing, preflight); with no ID it goes back
to the previous deployment that ran. It is audited as `kind: rollback`. A
rollback changes code and configuration only: Durable Object storage, D1
(migrations are forward-only), KV, R2 and queues keep their data, and
secrets are the workload's current ones (ADR-046).

mgmt now keeps the bundles of each workload's newest 10 deployments and
prunes older ones, keeping their record and hash (tw-mgmt
`-bundle-retention`, `-1` keeps every bundle). New API:
`GET /v1/tenants/{id}/workloads/{workload}/deployments` and
`POST /v1/tenants/{id}/workloads/{workload}/rollbacks`.

Operator action: none. The store migration adds columns to `deployments`.
The first deploy of each workload after the upgrade prunes that workload's
bundles beyond the newest 10; pass `-bundle-retention -1` to tw-mgmt
beforehand to keep them all.

#### deploy-plan-issue-73

`tw policy [dir]` prints everything an app needs in the tailnet policy, as
one snippet, before it is deployed. It runs the same check as `tw deploy`'s
preflight and deploys nothing. The snippet holds the missing `tagOwners`, a
grant for each `tailnet.connects`, the Tailscale Service auto-approval and
the public-ingress `nodeAttrs`, plus the grant that lets people call the app
(which tailworks can't check). It exits `acl_denied` while a checked part is
missing, so CI can run it before a deploy. `tw init` and the README's first
deploy now use it, so a first deploy takes one policy edit. With `--json`
the result is `$defs/policy`.

#### design-component-upgrade-release-process

You can now plan, check and undo upgrades (ADR-041).

- **Versions everywhere.**
  - Every binary carries the release version (`--version`).
  - Hosts report their version, protocols and features to mgmt, and mgmt
    replicas record their own versions.
  - `/v1/status` shows every version running in the fleet.
- **Skew policy.** A host may run one release line behind mgmt, but never
  ahead of it.
  - mgmt refuses a host outside that window with `version_skew`, and places
    nothing new on it.
  - The CLI warns you when it is too old or too new for mgmt.
- **Numbered mgmt migrations.**
  - The store records each migration it applies in `schema_migrations`.
  - A binary older than the store's minimum version refuses to start, so a
    downgrade can't corrupt the store.
  - `tw-mgmt migrate --check` lists the pending migrations.
- **Feature-gated placement.** A deployment that uses KV, R2 or D1 goes
  only to hosts that advertise that feature. If no host does, the deploy
  fails with `feature_unavailable`.
- **Host cordon and drain.** `tw host cordon`, `uncordon` and `drain`
  take a host out of placement and move its workloads off it before
  maintenance.
- **`tw upgrade`.**
  - It plans the upgrade, then backs up the store and custody together.
  - It installs and restarts the new release, then health-checks it.
  - It rolls back automatically if the health check fails.
  - `install.sh` keeps the previous install as `bin.prev` and `share.prev`,
    and `install.sh --rollback` restores it.
- **Release catalog.** tw-mgmt embeds the notes and upgrade facts of every
  release up to its own. `tw release import` adds a newer one, so
  `tw upgrade --plan` works offline.
- **Upgrade runs.**
  - mgmt drives a fleet upgrade host by host, starting with a canary and
    health-checking each host before the next.
  - The run keeps going if the CLI disconnects: it is stored in mgmt and
    driven by whichever replica holds its lease.
  - A host started with `tw-hostd -self-upgrade`, under a service manager,
    downloads the release tarball from mgmt, installs it and restarts.
    Other hosts show an operator step, and the run waits for them.
  - `tw upgrade status`, `pause`, `resume` and `abort` control the run.
- **Versioned data formats.** `objects.json`, node `object.json`,
  `tw.json` and the R2 index entries now carry a version. A `tw` reads older
  formats and refuses newer ones.

Operator action: none for a homelab. For a fleet, upgrade tw-mgmt before
any host. Hosts that predate version reporting count as 0.1.x, so they stay
within the window of a 0.2 tw-mgmt. See docs/upgrading.md.

#### docs-product-issue-51

Docs: runnable recipes (`docs/recipes.md`: internet APIs and LLMs through a tailnet gateway, a Durable Object per user authorized by `getCaller`, a signed public webhook, cron into KV), a CLI reference generated from every command's `--help` (`docs/cli.md`, found by `tw docs search`), a limits page with the code behind every limit (`docs/limits.md`), a README in every template, a one-page security model (`docs/security-model.md`) and a private disclosure process (`SECURITY.md`), and a user-facing `CHANGELOG.md` rendered from the release catalog. `tw docs search` now finds the README's quick start, the changelog and the security policy (`tw docs show ../README.md`).

#### implement-adr-15-head-scale

tailworks runs on a self-hosted **Headscale** control plane (ADR-015): `tw up --control=headscale` and `tw tenant add --control=headscale`. It needs Headscale v0.29 or later. There is no Funnel (public routes) and there are no Tailscale Services there; see docs/headscale.md.

#### implement-do-lease-locks-issue-26

Distributed locks and leader election on Durable Object leases: measured guarantees in docs/locks.md, and a `lock` template whose fencing tokens carry the lease epoch.

#### implement-queues-issue-18

Cloudflare **Queues** (wrangler `queues`): producers, push consumers, retries and dead-letter queues, on a sealed, lease-fenced broker in tw-mgmt (`tw queues`; ADR-039).

#### npm-packages-types-issue-41

**Typed templates and `tw types`.** Every `tw init` template has a `tsconfig.json` and `@cloudflare/workers-types` and `typescript` dev dependencies, so `npm install && npx tsc --noEmit` passes and editors know `getCaller`, `Request`, `DurableObjectState` and `ctx.tailnet` (`@tailworks/tailnet` now declares `ctx.tailnet` on `DurableObjectState` and `ExecutionContext`). New `tw types`, like `wrangler types`, writes `worker-configuration.d.ts` with the app's `Env` (vars, Durable Object, KV, R2, D1, Queue and Workflow bindings, secret names from `.dev.vars`, `.dev.vars.example`, `secrets.required` or `--remote`); `tw types --check` fails with `types_stale` in CI. The `@tailworks/*` packages and `create-tailworks` are ready to publish to npm, and `npm create tailworks` runs `tw init` when tailworks is installed.

#### platform-stores

Platform stores: a SQLite Durable Object per store, backed by a fenced, encrypted journal in tw-mgmt, so a store survives restarts and moves between hosts. KV, D1, R2 and Queues build on it.

#### preview-deploys-issue-45

`tw deploy --preview` runs a bundle next to the workload instead of
replacing it, as the workload `<workload>--preview-<alias>` (the alias is
`--preview-alias`, or the start of the bundle's SHA-256). A preview has the
workload's tags and secrets, but its own lease, Durable Object storage and
KV, R2 and D1 stores, which start empty; it joins no Tailscale Service, has
no public routes and runs one replica. Bundles that bind queues can't be
previewed (queues are tenant-wide). `tw previews <workload>` lists the
running previews. `tw promote <workload> <alias>` deploys the preview's
bundle to the workload through the same path as `tw deploy` (lease epoch,
host fencing, preflight), audited as `kind: promote`, then undeploys the
preview (`--keep-preview` keeps it). The preview's data is not copied
(ADR-048).

New API: `POST /v1/deployments` `preview` and `previewAlias`,
`GET /v1/tenants/{id}/workloads/{workload}/previews` and
`POST /v1/tenants/{id}/workloads/{workload}/promotions`.

#### r2-bucket-binding-issue-16

**R2** buckets (wrangler `r2_buckets`): the index is a platform store, and the bytes go to a host `fs` or `s3` backend (`-r2-backend`; `tw r2`; ADR-036). Bytes on the `fs` backend stay on their host.

#### workers-kv-binding-issue-15

**Workers KV** (wrangler `kv_namespaces`): the KV API inside Workers and `tw kv` for operators. Read-your-writes on the namespace's host (ADR-037).

#### workflows-approvals

Cloudflare **Workflows** (wrangler `workflows`) run on workerd's built-in engine, and `step.waitForApproval` is decided by tailnet identities holding `github.com/rosskukulinski/tailworks/cap/approve` (`tw approvals`, `tw approve`; ADR-040).

Operator action: workerd moves to 1.20261114.1.

#### wrangler-cli-integration-issue-19

tailworks works with the wrangler CLI: `wrangler.toml`, `--env`, and apps bundled by wrangler (`--wrangler`, `--from-wrangler-out`); see docs/wrangler.md.

### Fixes

#### docs-first-deploy-issue-48

Docs for a first deploy. The README quick start now grants callers access to the workload (without that grant, the `curl` hangs on any tailnet whose policy isn't allow-all), and `tw deploy` prints that grant next to the tagOwners lines it prints for new tags. New pages: `docs/troubleshooting.md` (a first deploy that doesn't answer, `identity_pending`, tagOwners after renaming an app, `peer_not_visible`, a `failed` deployment), `docs/concepts.md` (a glossary) and `docs/install-testing.md` (the Linux support matrix and measured timings, moved out of `docs/INSTALL.md`). INSTALL's intro is shorter, and its isolation notes and `tw dev` section come after the install steps.

#### fix-linux-ci-tests-issue-75

Two netd fixes found while making the test suite pass on Linux.

- **Joins after a restart no longer hang.** A node whose saved key control
  had expired could stay in `NoState`, retrying its login, without ever
  reaching NeedsLogin. Its rejoin then never ended, and it held a join slot
  while it waited. A login error that persists during a rejoin now counts as
  a rejection, and the object is re-minted. Join slots are now handed out in
  the order objects were created.
- **The first call to a just-created object no longer takes up to 5 s.** An
  object that calls one created a moment ago now waits for the new node to
  answer a disco ping, not just for a path to it. Without this, the first
  WireGuard handshake could arrive before the new node knew the caller, and
  was retried only after 5 s.

#### fix-orphan-workerd-issue-44

`tw dev` no longer leaves workerd running and `/tmp/tw-dev-*` behind when it is killed with SIGKILL (an IDE, a coding agent or a CI timeout). A small watchdog stops its workerd and node processes and removes the dir however `tw dev` exits, and the next `tw dev` cleans up after any earlier run whose process is gone. Another `tw dev` that is still running is left alone. The hostd and netd test helpers do the same for their workerd processes (#44).

#### fix-web-ui-refresh-jitter-restyle

Admin UI: no more jitter on refresh, a more readable layout, and it uses only the public `/v1` API.

#### prelaunch-102-license-notices

Release tarballs now include the project's `LICENSE` and a `THIRD_PARTY_NOTICES` file. The notices cover workerd, the Go modules compiled into the binaries, the vendored JavaScript packages and, where a tarball includes the microVM guest, Firecracker, the Linux kernel and the guest image's Ubuntu packages. Tarballs with a microVM guest also include `share/tailworks/vm/KERNEL-SOURCE.md` and `ROOTFS-SOURCE.md`, which say where to get the corresponding source and include a written offer for it. The guest image now carries the Debian copyright files of the Ubuntu packages it takes files from (in `/usr/share/doc`), and its `manifest.json` lists those packages (#102).

#### prelaunch-104-update-check

`tw init` and `tw dev` no longer run the once-a-day update check, so they make no outbound request (before, they could ask `api.github.com` for the latest release after they finished). `tw version --json` adds `updateCheck`: whether this tw runs the check (a release build without `CI` or `TW_NO_UPDATE_CHECK` set). The README ("What tw sends") and `docs/security-model.md` ("Telemetry and the update check") now say what the check sends, when it runs and how to turn it off; tailworks has no other telemetry.

#### prelaunch-93-audit-chain

`tw audit` and audit verification no longer report an intact chain as
broken while records are being appended (#93). Verification read a chain's
records and its head in two separate queries, so a record appended between
them made the head look one record ahead and the chain failed with "chain
head mismatch" (`audit_unverified`). Both are now read in one read
transaction. Detection of real truncation and tampering is unchanged.

#### upgrade-prefix-health-issue-63

`tw upgrade` on a `tw up` machine now replaces the install that runs the state dir (the binary the service or the running supervisor was started from, or the state dir itself), not whichever directory the `tw` binary happens to live in. Running it from an extracted release, as the first upgrade from 0.1.0 needs, used to plan against the extracted directory; it now refuses with `prefix_mismatch` unless `--prefix` names the install. docs/upgrading.md covers the first upgrade.

The health check after the restart now only waits for deployments that were active before the upgrade. One that had already failed no longer rolls a good upgrade back; `--plan` and the start of the run warn about it, and the result lists it (`notActiveBefore` in `--json`) (#63).

#### workerd-restart-issue-43

A workerd that crashes after its deployment has served is now restarted until it comes back. hostd retries with exponential backoff (200 ms doubling to 5 min) and never gives up. Before, six quick exits marked the deployment `failed` for good. Before each restart hostd kills the old workerd's process group and waits for it to empty. The stale ingress socket is then removed, so a restart no longer fails with `bind(): Address already in use`. hostd also no longer misses an exit when a child process still holds workerd's log. From the second exit in a row the deployment reports a new status, `crashloop`, and its error gives the attempt count, the next delay and the last log lines. `tw ls`, `--json`, the console and the API show this status. The deployment goes back to `active` by itself once workerd answers again. `tw deploy` stops waiting with `deploy_crashloop` when the deployment enters a crash loop. A deploy whose workerd never becomes healthy (a bad bundle or config) still ends `failed` within a few seconds.

Operator action: clients that match on deployment status should treat `crashloop` as "assigned here, not serving right now". A deployment left `failed` by this bug on an older host needs one `tw deploy` after the upgrade.

## v0.1.0 (2026-11-15)

workerd 1.20261108.1.

### Features

#### initial

First release. It includes `twd up` (the all-in-one homelab mode),
`twd dev`, and twd-mgmt and twd-hostd on the process and microVM tiers,
running the pinned workerd.
