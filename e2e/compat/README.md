# The Cloudflare compatibility suite (`make e2e-compat`)

[COMPAT.md](../../docs/COMPAT.md) says, surface by surface, what of the
Workers platform works on tailworks, with a test for each row. Those tests
are written for tailworks. This suite asks the other question: **does code
written for Cloudflare, by Cloudflare, run?**

It takes 30 example projects from Cloudflare's own repositories, each pinned
to a commit, and deploys them unmodified with `tw deploy` to a real
`tw-hostd` host joined to a real tailnet (tailnet 1, production control,
`tw up --oauth-client-file`). Then a tagged tsnet client node calls each
workload over the tailnet the way the project's README calls it on
Cloudflare: pages, JSON APIs, form posts, WebSockets, MCP sessions, cron and
queue consumer output in `tw logs`.

```sh
make e2e-compat                                    # everything (~15 min warm)
TW_COMPAT_ONLY='^(d1-|chanfana)' make e2e-compat   # some (a regexp on the ID)
make e2e-compat-prepare                            # fetch + install + build only, no tailnet
make e2e-compat-dev                                # the upstream SDK examples under tw dev: no tailnet, no credentials
```

The Python examples that need packages need [`uv`](https://docs.astral.sh/uv/) on `PATH`: their build step is the README's `uv run pywrangler sync`, which vendors them into `python_modules/` (it downloads a Python and the packages). It needs `npm` 11 or later (Node 24: with npm 10 the `ag-webmcp-react`
example's dependencies don't install) and the tailnet-1 key file
(`make e2e-compat` skips without it). `TW_COMPAT_PAR` deploys that many projects at once (default 3).
The results go to `e2e/real/results/compat-<time>-<run>.json` (one record
per project: outcome, timings, every request the probe made, deploy
warnings, workarounds). The run's log ends with the matrix as a table.

## What it runs

| ID | Upstream | What | Exercises (COMPAT.md) | Expect |
|---|---|---|---|---|
| `c3-hello-world` | workers-sdk C3 `hello-world/ts` | Worker only (TypeScript) | fetch | pass |
| `c3-hello-world-js` | workers-sdk C3 `hello-world/js` | Worker only (JavaScript) | fetch | pass |
| `c3-worker-assets` | workers-sdk C3 `hello-world-with-assets/ts` | Worker + static assets | assets, fetch | pass |
| `c3-assets-only` | workers-sdk C3 `hello-world-assets-only` | static site, no Worker | assets | pass |
| `c3-durable-object` | workers-sdk C3 `hello-world-durable-object/ts` | Durable Object RPC | do-rpc, do-storage | pass |
| `c3-durable-object-assets` | workers-sdk C3 `hello-world-durable-object-with-assets/ts` | Durable Object + assets | do-rpc, assets | pass |
| `c3-openapi` | workers-sdk C3 `openapi/ts` | Hono + chanfana OpenAPI | fetch | pass |
| `c3-scheduled` | workers-sdk C3 `scheduled/ts` | cron `* * * * *` + internet fetch | cron, fetch | pass |
| `c3-queues` | workers-sdk C3 `queues/ts` | queue producer + consumer | queues, queue-consumers | pass |
| `c3-workflows` | workers-sdk C3 `hello-world-workflows/ts` | Workflow: step.do, waitForEvent | workflows | pass |
| `c3-python` | workers-sdk C3 `hello-world/py` | Python Worker (Pyodide) | python | pass |
| `hello-world-do` | templates `hello-world-do-template` | Durable Object + SQLite | do-storage, do-rpc | pass |
| `nodejs-http-server` | templates `nodejs-http-server-template` | `node:http` server via `httpServerHandler` | nodejs-compat | pass |
| `d1-template` | templates `d1-template` | D1 + migrations | d1, d1-migrations | pass |
| `d1-sessions` | templates `d1-starter-sessions-api-template` | D1 sessions, bookmarks, assets | d1, d1-sessions, assets | pass |
| `chanfana-openapi` | templates `chanfana-openapi-template` | REST CRUD on D1 | d1, d1-migrations | pass |
| `openauth` | templates `openauth-template` | OpenAuth issuer: register → code in logs → auth code | kv, d1, d1-migrations, nodejs-compat, logs | pass |
| `r2-explorer` | templates `r2-explorer-template` | R2 Explorer: basic auth from a secret, bucket API, SPA | r2, assets, secrets | pass |
| `durable-chat` | templates `durable-chat-template` | PartyServer chat: WebSocket broadcast, SQLite history | websockets, hibernation, do-storage, assets, wrangler-bundling | pass |
| `multiplayer-globe` | templates `multiplayer-globe-template` | PartyServer WebSockets, `request.cf` geo | websockets, assets, wrangler-bundling | pass |
| `workflows-starter` | templates `workflows-starter-template` | Workflow start → waiting → event → complete, Vite SPA | workflows, do-storage, websockets, assets | pass |
| `to-do-list-kv` | templates `to-do-list-kv-template` | Remix on KV: form post, list | kv, assets, nodejs-compat | pass |
| `vite-react` | templates `vite-react-template` | React SPA + Hono API (Vite plugin) | assets, fetch | pass |
| `react-router-ssr` | templates `react-router-starter-template` | React Router 7 SSR, `vars` | vars, assets, nodejs-compat | pass |
| `astro-blog` | templates `astro-blog-starter-template` | Astro + `@astrojs/cloudflare` | assets, nodejs-compat | pass |
| `microfrontend` | templates `microfrontend-template` | router over service bindings | service-bindings | fails |
| `text-to-image` | templates `text-to-image-template` | Workers AI | workers-ai | fails |
| `remote-mcp-authless` | ai `demos/remote-mcp-authless` | remote MCP server (`agents/mcp`) | mcp, agents-sdk, nodejs-compat | pass |
| `mcp-server` | agents `examples/mcp-server` | stateless MCP (`@modelcontextprotocol/server`) | mcp | pass |
| `mcp-agent` | agents `examples/mcp` | stateful `McpAgent` (state in a Durable Object) | agents-sdk, mcp, do-storage, assets | pass |

| `mcp-server`, `mcp-agent`, `ag-*` | agents `examples/` | [the upstream SDK examples](#the-upstream-sdk-examples-issue-325) | agents-sdk, mcp, … | see COMPAT.md |
| `mcph-*` | vercel/mcp-handler `examples/` | the same | mcp | see COMPAT.md |
| `ai-*` | vercel/ai `examples/` | the same | vercel-ai | see COMPAT.md |
| `py-query-d1` | python-workers-examples `query-d1` | Python Worker on D1, seeded with `tw d1 execute --file db_init.sql` (deployed run only) | python, d1 | pass |
| `py-fastapi-todo`, `py-flask-todo` | python-workers-examples `fastapi-todo`, `flask-todo` | Todo-Backend on D1 (FastAPI, Flask), packages vendored by `uv run pywrangler sync`, seeded with `tw d1 execute` (deployed run only) | python, d1 | pass |
| `py-*` | cloudflare/python-workers-examples | [the Python examples](#the-python-workers-examples-issue-292) | python, … | see COMPAT.md |

"refused": `tw deploy` must refuse it with an error that names the
surface. "fails": it deploys, and its probe must fail (a binding tailworks
doesn't provide). If one of those starts passing, that's an **XPASS** and the
run fails, so that COMPAT.md and `projects.go` get updated together.

The last run's numbers are in [`status/cloudflare-examples-compat-suite.md`](../../status/cloudflare-examples-compat-suite.md)
(or the newest results file).

## The upstream SDK examples (issue #325)

`upstream.go` adds the examples of the three SDK projects the README's
"run unmodified" sentence is about: `cloudflare/agents` `examples/` (the
playground is one of them), `vercel/mcp-handler` `examples/` and the Vercel
AI SDK's `examples/`. A project with a `Section` is one of these.

They run twice, with the same probes:

- **`make e2e-compat-dev`** (`TestCompatDev`): each example under
  `tw dev --json`, probed on 127.0.0.1. No tailnet, no credentials, about
  20 s warm. This is the run the GitHub nightly does
  (`.github/workflows/nightly.yml`), and it writes the tables it gets to the
  job summary.
- **`make e2e-compat`**: deployed on tailnet 1 with everything else.

Every example of the three projects is accounted for: the ones that run are
projects, the rest are in `UpstreamNotRun()` with the reason. The results
are **published in [COMPAT.md](../../docs/COMPAT.md#upstream-examples-agents-sdk)**,
one row per example, and those tables are generated:

```sh
make e2e-compat-dev && make e2e-compat   # or TW_COMPAT_ONLY=… for some
make compat-upstream                     # newest results → upstream-results.json + the COMPAT.md tables
git add e2e/compat/upstream-results.json docs/COMPAT.md
```

`make compat-upstream-check` (in `make lint`) fails when a table differs
from what `upstream-results.json` and the project list render, when a
published project has no recorded `tw dev` result, and when a recorded
result is for another commit of the example than the one pinned now. So a
row can't be edited by hand, and an example can't be added or re-pinned
without running it. The date in each table is the date of the run that was
committed; the nightly's job summary has last night's.

What the published result means:

- **pass**: no `Changes`, the probe passed in every mode it ran in.
- **pass with change**: the probe passed, and the project lists `Changes`:
  what the run changed in the example itself (its config, an added Worker
  entry, a scripted model). `Workarounds` are install mechanics (a
  workspace dependency pinned to its published version, `npm install`
  outside the monorepo) and don't make a pass "with change".
- **fail**: an `Expect` (both modes) or `ExpectDeployed` (deployed only),
  with the issue it is tied to. The run stays green while it fails the
  expected way, and reports XPASS when it starts passing.
- **not run**: `UpstreamNotRun()`.

The Vercel AI SDK examples call OpenAI. The suite never calls a model: the
Worker entry it adds (`tw-compat-openai.ts`) answers the example's
Responses API calls with a scripted stream. That is listed as a change.

### What the upstream run found (2026-11-24)

1. **Fixed ([#354](https://github.com/rosskukulinski/tailworks/issues/354)): a pre-built Worker that imports `cloudflare:email` couldn't start.**
   The Agents playground does, for its email demos. It starts now, and 8
   of the probe's 9 demo families pass. The ninth,
   `SupervisorAgent.createChild` (agent-to-agent Durable Object RPC),
   failed with `Could not serialize object of type "RpcPromise"` until
   [#391](https://github.com/rosskukulinski/tailworks/issues/391): inside
   an object, `env.X` had only `getByName`, so `getAgentByName`'s
   `env.X.get(env.X.idFromName(name))` passed a pending RPC result to
   `get()`. All 9 pass now.
2. **Open ([#355](https://github.com/rosskukulinski/tailworks/issues/355)): agents reached through the imported env are not created.**
   `cross-domain` and `mcp-elicitation` pass
   `import { env } from "cloudflare:workers"` to the Agents SDK and get
   `create_not_admitted`.
3. **Fixed ([#356](https://github.com/rosskukulinski/tailworks/issues/356)): the built-in bundler and CommonJS `require()` of a Node builtin.**
   A Worker with `dotenv` in it bundled and then couldn't start. With
   `nodejs_compat` the built-in bundler now turns such a `require()` into
   an import, as wrangler does, and the three AI SDK examples use it
   instead of `--wrangler`.
4. **Fixed ([#359](https://github.com/rosskukulinski/tailworks/issues/359)): an `AgentWorkflow` started with `runWorkflow` stayed queued on a deployed host.**
   The Agent's class wasn't in `tailnet.objects`, so mgmt refused its node,
   and the Workflow was refused the Agent. The `workflows` example now
   passes deployed too.
5. **Fixed ([#360](https://github.com/rosskukulinski/tailworks/issues/360)): no HTTPS origin on the tailnet.**
   workers-oauth-provider refuses an `http://` resource URL. With
   `"tailnet": {"https": true}` the workload is also served on its
   `https://` tailnet URL, and the deployed run of
   `mcp-worker-authenticated` sets that (`Project.TailnetHTTPS`) and calls
   it there. It passes under `tw dev` (loopback); the deployed run hasn't
   been recorded since.
6. Upstream quirks: `@hono/node-server` replaces the global `Request` and
   `Response`, which no Workers runtime survives (stock `wrangler dev`
   fails the same way), so the Hono example's `serve()` is aliased to a
   shim. The `workflows` example throws after each step 9 times in 10 on
   purpose, so its approval step isn't reached.

### The second pass (2026-11-24)

`upstream_more.go` adds the 9 `examples/next/*` examples that declare no
Cloudflare-only binding and the AI SDK's `fastify`, `nest` and `mcp`
(its `http` and `sse` servers). Under `tw dev` all 9 `next/` examples
pass, and the `mcp` `http` server passes with the same entry the other Node
servers get. Two of the `next/` examples first failed on tailworks gaps,
fixed since:

1. **Fixed ([#395](https://github.com/rosskukulinski/tailworks/issues/395)): a Durable Object that called `ctx.abort()` was never recreated.**
   `next/lifecycle`'s `/restart` aborts its object; every later request to
   it failed with the abort reason.
2. **Fixed ([#391](https://github.com/rosskukulinski/tailworks/issues/391)): `RpcPromise` couldn't be serialized.**
   `next/routing`'s hub (`RoutedAgents.get()`, then a call on the chat's
   stub) failed like the playground's `createChild`.
3. Not tailworks: Fastify never registers its server with the node:http
   bridge (and doesn't start under stock `wrangler dev`); Nest `require()`s
   optional packages a bundler can't resolve (stock `wrangler dev` fails the
   same way); the `mcp` `sse` server writes to one request's response while
   handling another, which no Worker can (stock `wrangler dev` cancels it
   the same way).

The Agents SDK examples that need Workers AI stay not run: tailworks
ignores the `ai` binding, so a scripted `env.AI` would have to be wired
into each example (its config, or a wrapper around each agent class).

## What "unmodified" means

The suite does to each project only what a user following its README
does, plus one file:

1. **Fetch** the pinned commit (a blobless partial clone, cached in
   `~/.cache/tailworks-compat`, or `$TW_COMPAT_CACHE`) and copy the
   project directory.
2. For **create-cloudflare templates**, fill `<WORKER_NAME>`,
   `<PACKAGE_NAME>` and `<COMPATIBILITY_DATE>` as `npm create cloudflare`
   does.
3. **`npm ci`** (or `npm install` without a lockfile) with
   `--ignore-scripts`, because the code is fetched, not reviewed. Then the
   README's **build** (`npm run build`), if it has one.
4. Write **`tailworks.jsonc`** with only the workload's name and the run's
   tag, `tag:tw-wl-compat`. Without it each workload would get its own
   `tag:tw-wl-<name>`, which needs one tagOwner per project in the tailnet
   policy. Durable Object classes stay internal (`"node": false`), as they
   are without the file.
5. **`tw secret put`** for the README's `wrangler secret put`, then
   **`tw deploy`**, then the README's post-deploy steps
   (`wrangler d1 migrations apply` → `tw d1 migrations apply`).

Anything beyond that is a **workaround**. It's listed in the project's
`Workarounds` and in the results, and it's a tailworks gap or an upstream
quirk to look at:

- **Vite plugin projects** (`react-router-ssr`, `workflows-starter`;
  `mcp-agent` and the `ag-*` examples deploy plainly now that #301 is
  fixed; [#301](https://github.com/rosskukulinski/tailworks/issues/301)): the build writes `.wrangler/deploy/config.json`, which
  points at a generated `dist/<worker>/wrangler.json` (`no_bundle`, ESModule
  `rules` over pre-split chunks). `wrangler deploy` follows it. `tw deploy`
  reads the project's own config, and its built-in bundler refuses
  ESModule rules. So the suite runs `tw deploy <generated dir> --wrangler`.
  (`vite-react` doesn't need this: its own config already points at the
  build output.)
- `mcp-agent`, `mcp-server` and `remote-mcp-authless` live in pnpm
  monorepos without lockfiles of their own, so they get `npm install`, with
  `agents` pinned to the published version (plus its peer
  `@modelcontextprotocol/server`, which the monorepo hoists).
- `to-do-list-kv`: `npm ci --legacy-peer-deps`. The template's lockfile
  pins wrangler 4 against `@remix-run/dev`'s optional wrangler 3 peer, and
  npm 11 refuses that (an upstream problem; `npm run deploy` hits it too).

Not a workaround: `durable-chat` and `multiplayer-globe` bundle with
`tw deploy --wrangler`, because their client bundle is wrangler's
`build.command`, which only the wrangler bundler runs (as documented in
[wrangler.md](../../docs/wrangler.md#bundling)).

## The tailnet

The run follows `e2e-public`'s rules. The API key is used only to create and
delete the run's OAuth client, mint the client node's key, apply the policy
additions and clean up by ID. The only policy entries the run may add are:

```jsonc
"tagOwners": { "tag:tw-wl-compat": ["autogroup:admin", "tag:tw"] },
"grants":    [{ "src": ["tag:ttest"], "dst": ["tag:tw-wl-compat"], "ip": ["tcp:80", "tcp:443"] }]
```

`tcp:443` is for the projects deployed with `tailnet.https`
(`ag-mcp-worker-auth`). If anything else is missing, the run refuses to write the policy. Every
node it creates is named `ttest-compat-<run>-*` / `tw-compat-<run>-*`; the
cleanup deletes them by ID and fails the run if any are left.

## Adding a project

Add an entry to `Projects()` in `projects.go`, in the section for its repo:

- `ID` is the workload name.
- Pin `Commit` (the repo's constant).
- List the COMPAT.md anchors it `Exercises`. `Validate` checks that they
  exist.
- Write a `Probe` that does what the README says to try. Use `r.Get`,
  `r.PostJSON`, `r.WS`, `r.MCPConnect` and `r.WaitLog` (in `probe.go`).

`make e2e-compat-prepare TW_COMPAT_ONLY=<id>` checks that it installs and
builds, and `TW_COMPAT_ONLY=<id> make e2e-compat` runs it. A gap is an
entry with an `Expect` (`ExpectRefused` or `ExpectProbeFail`) and the
COMPAT.md row it belongs to. The suite promises at least 20 projects that
are expected to pass, and `Validate` enforces that.

## The Python Workers examples (issue #292)

`python.go` adds every example in
[cloudflare/python-workers-examples](https://github.com/cloudflare/python-workers-examples)
in the same way (`Section` `python-workers`, published in
[COMPAT.md](../../docs/COMPAT.md#upstream-examples-python-workers)). The ones
that need no vendored package run unmodified; the ones tailworks refuses
(crons, Workflows, assets, an un-vendored `workers` SDK) must be refused with
the message COMPAT.md names; the rest are in `pythonNotRun()` with the reason.
The examples that need packages build with `uv run pywrangler sync` (their
README's step). `py-query-d1`, `py-fastapi-todo` and `py-flask-todo` need
their database seeded after the deploy (`d1 execute --file db_init.sql`), so
only the deployed run (`make e2e-compat`) runs them. Examples whose only
post-deploy step is `d1 migrations apply` run under `tw dev` too, which
applies migrations itself. The first Python project of a run fetches the
pinned Pyodide bundles once into the per-user cache (`internal/pyodide`).

