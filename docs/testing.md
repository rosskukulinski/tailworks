# Testing a tailworks app

A tailworks app is a Cloudflare Worker (plus Durable Objects) whose callers are
tailnet identities. Test it at two levels:

| Level | Runs in | Use it for | Tools |
|---|---|---|---|
| Unit | workerd (vitest-plugin) or Node | your Worker and Durable Object logic, with callers and `ctx.tailnet` faked | [`@cloudflare/vitest-plugin`](#unit-tests-with-cloudflarevitest-plugin), [`@tailworks/tailnet/testing`](#twtailnettesting) |
| Black box | a real local tailworks (`tw dev`) | the app as deployed: `getCaller` verified by netd, real `ctx.tailnet`, per-object nodes, approvals, typed denials | [`tw dev --caller`, `@tailworks/tailnet/testing/dev`](#black-box-tests-tw-dev---caller-and-startdev) |

Every template ships one black-box test, `test/app.test.js`, and an
`npm test` script that runs it:

```sh
tw init myapp --template lock
cd myapp && npm install
npm test            # needs tw on PATH (or TW_BIN=/path/to/tw)
```

The test imports `@tailworks/tailnet/testing/dev`, so `npm install` must
succeed first. Each tailworks release publishes `@tailworks/*` to npm at its
version ([wrangler.md](wrangler.md#types-tw-types)).

## Black-box tests: `tw dev --caller` and `startDev`

`tw dev` runs your app on the same tw-hostd, netd and workerd as a
deployment, against an embedded control plane on 127.0.0.1. By default it
calls the app from one dev node ("you"). `--caller` adds more identities,
each its own dev node with its own local URL:

```sh
tw dev --port 0 --json --caller alice --caller ci=tag:ci
```

The `ready` event (and `tw dev --once --json`) lists them:

```json
"callers": [
  {"name": "alice", "url": "http://127.0.0.1:57174", "as": "user-4@fake-control.example.net", "nodeId": "TESTCTRL00000004", "node": "dev-alice.ttest.ts.net"},
  {"name": "ci", "url": "http://127.0.0.1:57175", "tags": ["tag:ci"], "nodeId": "TESTCTRL00000003", "node": "dev-ci.ttest.ts.net"}
]
```

A request to a caller's URL reaches the app over that caller's node, so
`getCaller(req)` returns that node, verified by netd as in production.
Nothing is forged. A caller without tags is a user's node (`user.login` is
set); one with tags has no user.

`@tailworks/tailnet/testing/dev` wraps this for Node tests. `startDev()` starts
`tw dev <dir> --port 0 --json` as a child process, waits for `ready`, and
`stop()` sends SIGTERM and waits for it to exit (tw dev stops its own
workerd). Children still running when the test process exits are killed.

```js
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import { startDev } from "@tailworks/tailnet/testing/dev";

let app;
before(async () => {
  app = await startDev({ dir: ".", callers: ["alice", "bob"] });
});
after(() => app?.stop());

test("only the holder can release", async () => {
  const alice = app.as("alice"), bob = app.as("bob");
  const { token } = await (await alice.fetch("/locks/x/acquire", { method: "POST" })).json();
  const r = await bob.fetch("/locks/x/release", { method: "POST", body: JSON.stringify({ token }) });
  assert.equal(r.status, 403);
});
```

`startDev` options: `dir`, `callers` (names, or `{name: {tags}}`), `tw` (the
binary; default `$TW_BIN`, then `tw`), `args` (more `tw dev` flags, such
as `["--env", "staging"]`), `env`, `readyTimeoutMs`, `stopTimeoutMs`,
`onLog`. Each caller has `url`, `login` or `tags`, `nodeId`, `node` and
`fetch(path, init)`; `app.fetch` calls as "you".

Limits of `tw dev` today:

- The embedded control plane names users itself (`user-<n>@…`). You can't
  pick a caller's login, so compare against `caller.login` / `caller.nodeId`
  instead of a fixed string. To test logic that depends on a specific login,
  use a [unit test with `withCaller`](#twtailnettesting).
- There's no policy evaluator yet: every dev node can reach every other, and
  the only app capability granted is a test one. A tool or approval gated on
  your own capability is refused (`cap_missing`), which is what the `mcp` and
  `refund-approval` templates' tests check. Allowed paths under a grant need
  a unit test, or a real tailnet.

## Unit tests with `@cloudflare/vitest-plugin`

[`@cloudflare/vitest-plugin`](https://developers.cloudflare.com/workers/testing/vitest-integration/)
(formerly `@cloudflare/vitest-pool-workers`) works with a tailworks app's
`wrangler.jsonc` as is. Measured with `@cloudflare/vitest-plugin` 1.8.4 and
vitest 5.1.1 on the `lock`, `agent`, `mcp` and `refund-approval` templates
(`make test-vitest`, `e2e/vitest`):

| Works | Notes |
|---|---|
| Worker `fetch`, `exports.default.fetch` / `SELF` | |
| Durable Objects: storage (KV and SQLite), alarms, `runInDurableObject`, `runDurableObjectAlarm` | |
| Cloudflare Agents SDK: state, SQL, `routeAgentRequest` | `agent`, `refund-approval` |
| Workflows binding: a Workflow instance starts and waits | `refund-approval` |
| `createMcpHandler` + `@tailworks/mcp` authorization (caps, limits, budget DO) | with `withCaller({caps})` |
| `request.cf` from `new Request(url, { cf })` | survives a service binding and a Durable Object stub's `fetch()`, so `withCaller` reaches `getCaller` in your objects |

What it can't do on its own, because these are tailworks', not workerd's:

- **`getCaller(req)`** throws `no_identity`: nothing delivers a caller.
  Use `withCaller` (below).
- **`ctx.tailnet`** is absent (`tailnet(ctx)` throws `no_identity`; the `lock`
  template falls back to epoch 0). Attach the fake one (below).
- **Approvals** (`waitForApproval` decided by a tailnet identity) and
  per-object egress: only on tailworks. Use a black-box test.

Its miniflare brings its own workerd build; it doesn't use tailworks' pinned one.

A `vitest.config.ts` for a tailworks app:

```ts
import { cloudflareTest } from "@cloudflare/vitest-plugin";
import { defineConfig } from "vitest/config";

export default defineConfig({
  plugins: [cloudflareTest({ wrangler: { configPath: "./wrangler.jsonc" } })],
});
```

## `@tailworks/tailnet/testing`

Works in workerd (vitest-plugin) and in Node.

```ts
import { exports } from "cloudflare:workers";
import { runInDurableObject } from "cloudflare:test";
import { createTestTailnet } from "@tailworks/tailnet/testing";

const tw = createTestTailnet({ self: { epoch: 7 } });

// a request getCaller() sees as alice's node
const r = await exports.default.fetch(tw.withCaller("http://app/locks/a/acquire", { login: "alice@example.com" }));

// a tagged caller with an app capability
tw.withCaller(req, { tags: ["tag:agent"], caps: { "example.com/cap/mcp": [{ tools: ["lookup_order"] }] } });

// a public (Funnel) request: getCaller throws public_request, getRequester says public
tw.withPublic(req, { ip: "198.51.100.1" });

// ctx.tailnet on a Durable Object (records spawn, kill, audit; answers self())
await runInDurableObject(env.LOCK.getByName("a"), (_obj, state) => { tw.attach(state); });
// ctx.tailnet for a Worker handler
await worker.fetch(req, env, tw.ctx());

tw.spawned;  // [{class, name, caps, ttl, principal, objectId}]
tw.killed;   // [{reason, cascade, target, killed}]
tw.audits;   // records passed to ctx.tailnet.audit()
tw.fetches;  // requests sent through ctx.tailnet.fetch()
```

`withCaller(req, who)`: `who` is `{login}` (a user's node) or `{tags}` (a
tagged node), never both, plus optional `nodeId`, `hostname`, `node`,
`addrs`, `caps`, `tw`, `capsWithheld`, `objectId`, `traceId`. Defaults are
derived from the login or tag (`alice@example.com` → node ID `test-alice`,
node `alice.test.ts.net`).

`createTestTailnet(opts)`: `self` (what `self()` answers; default workload
`test-workload`, deployment `test-deployment`, epoch 1),
`fetch` (answers `ctx.tailnet.fetch`; by default every destination is
refused with `forbidden_destination`, and any response with a `tw-error`
header is thrown as a `TailnetError`, as on tailworks), `onSpawn` (throw to fail a
spawn), and `env` + `bindings` (to name spawned classes). A spawn's `stub` is
the real one from `ns.getByName(name)`, so the child object runs.

### Why this can't weaken a deployed app

On tailworks, `request.cf` is not something a client can send. netd, the only
process that can connect to workerd's ingress socket, drops any `x-tw-cf`
header a client sent and writes its own from WireGuard and WhoIs; workerd
turns that header into `request.cf` (`cfBlobHeader`) and removes it. The
runtime shim (`__tw_rt.js`) and `getCaller` only read `request.cf`.

`withCaller` adds no header and opens no socket. It returns a new in-memory
`Request` with `cf` set, in the test's own process; a request that arrives
over the network never goes through it. App code could already build such a
request with `new Request(url, { cf })`, so the helper adds no capability to
a deployed app. As a guard against a stray import, the top-level
`withCaller()` / `withPublic()` throw unless the test opted in with
`enableTestCallers()` or `createTestTailnet()`.

## The Node runtime

The `node-workflow` template's test is a black-box test too
(`startDev`, `/whoami` as a caller). `@tailworks/node` verifies a signed caller
header; there's no `withCaller` for it yet.
