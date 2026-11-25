Last update: 2026-11-24 PT. Issue #325: the upstream examples of cloudflare/agents, vercel/mcp-handler and the Vercel AI SDK, run under `tw dev` and deployed, published in COMPAT.md from the suite's results.

# Upstream SDK examples in the compatibility suite (#325)

## What's built

- `e2e/compat/upstream.go`, `upstream_probes.go`, `agentws.go`: 18 new
  projects in the existing `e2e-compat` suite (it already had `mcp-server`
  and `mcp` from cloudflare/agents, which are now published too, 20 in all),
  and `UpstreamNotRun()`, which lists every other example of the three
  projects with the reason. Pinned: cloudflare/agents `95dacf84dcdf`
  (`agents` 0.34.0), vercel/mcp-handler `5518e8af4386` (2.4.0), vercel/ai
  `8956ca55ed9c` (`ai` 7.0.181, `@ai-sdk/openai` 4.0.122).
- `e2e/compat/dev_test.go`: `make e2e-compat-dev` (`TestCompatDev`) runs
  those projects under `tw dev --json` with the probes the deployed run
  uses. No tailnet and no credentials; about 15 s warm on this Mac.
- Probe helpers: an Agents SDK WebSocket client with `@callable` RPC and
  streaming RPC, MCP elicitation over the call's SSE stream (2025) and
  multi-round-trip requests (MCP 2026-07-28), an OAuth 2.1 client
  (registration, PKCE, token), default request headers.
- `Project.Section`, `Changes`, `OriginVar` (a var the README says to set to
  the served URL: two deploys), `ExpectDeployed` (an expected failure only
  when deployed). An expected startup failure works under `tw dev` too.
- `e2e/compat/report.go`, `compatreport/`, `upstream-results.json`: the
  three tables in `docs/COMPAT.md` are generated from the recorded results
  (`make compat-upstream`), and `make compat-upstream-check`, part of
  `make lint`, fails when they are edited by hand, when a published project
  has no recorded `tw dev` result, or when a result is for another commit
  than the pinned one.
- Nightly: `e2e-compat-dev` is in `.github/workflows/nightly.yml`'s matrix.
  It writes the tables it gets to the job summary and uploads the results
  file. The deployed half runs wherever `make e2e-compat` runs nightly (the
  new projects are part of it).
- Docs: COMPAT.md (the three SDK rows, the Agents SDK details rows for
  `McpAgent`, `@callable`, the imported env and `AgentWorkflow`, and the
  three generated sections), README (a pointer from the "run unmodified"
  sentence and two matrix notes), `e2e/compat/README.md`.
- `mcp-agent` no longer uses the Vite redirect workaround (#301 is fixed);
  it deploys plainly, as the new Vite-built examples do.

## Results (2026-11-24, darwin/arm64; tailworks `286f6a184247` under `tw dev`, `c97a6175a081` deployed)

| Upstream | Examples | pass | pass with change | fail | not run |
|---|---|---|---|---|---|
| cloudflare/agents `examples/` | 69 | 9 | 0 | 5 | 55 |
| vercel/ai `examples/` | 25 | 0 | 3 | 0 | 22 |
| vercel/mcp-handler `examples/` | 3 | 0 | 3 | 0 | 0 |

A row's result is the worse of its two runs. The five Agents SDK failures:
`cross-domain`, `mcp-elicitation` and `playground` fail in both modes;
`mcp-worker-authenticated` and `workflows` pass under `tw dev` and fail
deployed. Of the 55 not run, 38 declare a binding tailworks doesn't have
(Workers AI in 33 of them), 3 need a key or a paid external service, and 14
are the `examples/next/` sub-examples, 9 of which declare no Cloudflare-only
binding and are the next to add.

"Pass" means the probe passed. The probes do what each example's README or
client does; they don't cover everything an example can do. Named limits:
`mcp-client` connects to no MCP server (the run's tailnet policy lets the
workload reach no peer), `push-notifications` sends to a push endpoint that
doesn't exist (the send fails after VAPID signing), `workflows` doesn't
reach its approval step (the example throws after each step 9 times in 10
on purpose), and the React clients are fetched, not run in a browser.

## Findings

1. [#354](https://github.com/rosskukulinski/tailworks/issues/354): a
   pre-built (Vite, `no_bundle`) Worker that imports `cloudflare:email`
   can't start. The stub is only in the built-in bundler. The playground
   fails on this. Looking ahead with the import stubbed by hand (not a suite
   result): 8 of the 9 demo families the probe covers passed, and
   `SupervisorAgent.createChild` failed with `Could not serialize object of
   type "RpcPromise"`, which a smaller app built with the built-in bundler
   did not reproduce.
2. [#355](https://github.com/rosskukulinski/tailworks/issues/355): agents
   reached through `import { env } from "cloudflare:workers"` get
   `create_not_admitted`. Documented behaviour (create admission), and two
   of Cloudflare's examples are written that way. A plain Durable Object
   Worker using the imported env did create objects, so the doc's flat rule
   isn't the whole story.
3. [#356](https://github.com/rosskukulinski/tailworks/issues/356): the
   built-in bundler leaves `require("fs")` in a CommonJS dependency
   (`dotenv`) as a shim that throws at startup. `--wrangler` bundles it.
4. [#359](https://github.com/rosskukulinski/tailworks/issues/359): an
   `AgentWorkflow` started with `runWorkflow` stays queued on the deployed
   host (3 of 3 runs), and runs under `tw dev`. Plain Workflows passed in
   the same deployed runs. tw-hostd logged `pool node not created …
   unknown_namespace … "_pool"` once a second for that workload's class.
5. [#360](https://github.com/rosskukulinski/tailworks/issues/360):
   workers-oauth-provider requires an HTTPS resource URL and a tailnet
   workload is served over `http://`, so the OAuth-protected MCP example
   can't be configured for its tailnet URL.
6. Not tailworks: `@hono/node-server` replaces the global `Request` and
   `Response`, and the Hono example fails the same way under stock
   `wrangler dev` when served through `httpServerHandler`. Its `serve()` is
   aliased to a shim instead.

## Tests

The published results are from the last two runs below, on this branch
after merging `origin/master` (`b5b6d2f5`).

- `make e2e-compat-dev`: `--- PASS: TestCompatDev (17.80s)`, counts
  `PASS:17 XFAIL:3`.
- `TW_COMPAT_ONLY='^(ag-|mcph-|ai-|mcp-server$|mcp-agent$)' make e2e-compat`
  (tailnet 1): `--- PASS: TestE2ECompat (125.66s)`, counts
  `PASS:15 XFAIL:5`, cleanup `devices_left: 0`, no errors.
- `make e2e-compat` (all 48 projects, tailnet 1), before the merge, on
  `a7a7393cae68`: `--- PASS: TestE2ECompat (213.11s)`, counts
  `PASS:40 XFAIL:8`, cleanup `devices_left: 0`. The full run before that
  one had one FAIL, a harness bug: the prepared copy of
  `mcp-worker-authenticated` still carried the previous run's
  `MCP_RESOURCE`, so the first deploy failed instead of the second. The run
  now resets the var to the example's own value first.
- `make e2e-compat-dev` from an empty cache: prepared 20 projects in 1m41s,
  `--- PASS: TestCompatDev (118.72s)`.
- Linux (`gh workflow run nightly.yml` on this branch, ubuntu-24.04,
  amd64, Node 22): `PASS:16 XFAIL:3 FAIL:1`. The failure was
  `ag-webmcp-react`: npm 10 can't install its dependencies (`Cannot read
  properties of null (reading 'edgesOut')`; reproduced here with
  `npx npm@10`; `--legacy-peer-deps` installs but leaves out peers the
  build needs). The example's `engines` asks for Node 24, so the nightly
  job now runs on Node 24. The second run, on Node 24, was
  `PASS:16 XFAIL:3 FAIL:1` again, with a different failure, a race in the
  dev runner: `ai-express`'s `tw dev` found its port taken (`address
  already in use`), because the runner picks a free port before starting.
  It now retries with another port. The third run
  (`fa411f91904c`) passed: `PASS:17 XFAIL:3` on linux/amd64, with the
  tables in the job summary and the results file uploaded.
- `make lint`: ok (includes `compat-check`, `compat-upstream-check`,
  `go test ./e2e/compat/`, staticcheck). `make lint-docs test-docs`: ok.
- `make changes-check`: ok (no upgrade-relevant paths).
- Not run: `make test` (no Go outside `e2e/compat` changed), and
  `make templates-typecheck test-sdk` (no template, JS package or `e2e/sdk`
  change).

## What remains for #325

- The 9 `examples/next/*` examples that need no Cloudflare-only binding.
- The Agents SDK examples that need Workers AI: they could run with a
  scripted `env.AI`, as the AI SDK examples run with a scripted OpenAI API.
  That is a change to each example and was not done.
- The date in COMPAT.md moves when someone commits `make compat-upstream`
  output. The nightly publishes each night's tables in its job summary but
  doesn't commit them.
- The AI SDK's `fastify`, `nest` and `mcp` examples (Node servers and
  scripts) were not tried.
