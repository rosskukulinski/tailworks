Last update: 2026-11-21 PT. Fix #211 (ledger H-211): a killed plain DO's RPC methods run no tenant code.

# fix-211-killed-do-rpc

## Root cause

A plain DO (`"egress": "workload"`) runs in the tenant isolate as the
class `wrapPlainClass` builds (`js/packages/tailnet/src/runtime.ts`,
bundled into `hostd/gen/js/tw-rt.js`). The wrapper checked the kill
marker (`__tw_killed` in the object's storage) only in `fetch` and
`__tw_intercept`. workerd dispatches an RPC call straight to the class's
method, so every other method ran after `tw kill` reported success.
Through the guarded env, hostd's admit already refuses a killed name
(#153), so the bug showed through the raw binding
(`import { env } from "cloudflare:workers"`, which skips admit) and
through stubs kept from before the kill.

The facet path was already gated: `Host_<Class>`'s Proxy checks
`__killed` before forwarding any RPC to the facet.

## Fix

- `runtime.ts` `killGate`: the plain DO's constructor returns a Proxy over
  the object. Every method reached through it (RPC entry points, runtime
  handlers) checks the kill marker first. A killed object throws
  `[killed] object C/name was killed: …`, which the caller's handler
  already answers as `410 killed` (`asResponse`). `alarm` and the
  `webSocket*` handlers return without running. A getter is tenant code,
  so it throws too. `fetch`, `__tw_kill` and `__tw_intercept` keep their
  own handling.
- Methods run with `this` set to the real object, not the Proxy. That keeps
  private fields working, and the object's own `this.method` lookups keep
  their identity (the Agents SDK's `@callable` metadata is keyed by the
  function). Wrapping prototype methods instead would have broken that.
- `platform.js` `__facet()`: refuses a killed object. Every Host path to
  the facet already checks `__killed` first; this closes it at the one
  place they all meet.
- Fixture: `probe`'s `Ticker` has an RPC method `hello`, and the worker
  route `/tickrpc?name=…[&raw=1]` calls it.

## Tests

- `go test -run 'TestHostd/KillStopsPlainDORPC' ./hostd/`: **failed on
  master** (`RPC after kill&raw=1: 200 map[reply:map[hello:worker …]]`);
  passes with the fix. All of `TestHostd` passes (18 s).
- `node --test test/killrpc.test.js` (js/packages/tailnet): 2 of 4 failed
  against master's runtime (kept stub and fresh stub ran tenant code; no
  410 through the handler), all 4 pass after. The full JS suite passes
  (42/42).
- `make lint`: ok. `make changes-check`: ok (fragment
  `changes/fix-211-killed-do-rpc.md` added, kind security).
- `make test` (mbp runner): ok, exit 0.
- `make test-sdk` (mbp runner): ok. That includes `TestCloudflareAgents`, a
  plain-DO Agents SDK app, so the Proxy works with the SDK's callables.
- `make templates-typecheck`: ok.
- `make e2e-locks`: ok. lockpulse uses plain DOs.
- Skipped: the container and VM suites (`ctr` is a plain DO with a
  container). They need Docker or KVM.

## Gaps

- The Proxy gates calls from outside the object. Code that runs inside the
  object (a timer or promise it started before the kill) is not stopped.
  That is the same as for `fetch`. Stopping it would need the isolate to
  abort the object (`ctx.abort()`).
- A facet Host's own helper methods (`__admitted`, `__ensure`, `__facet`)
  are on its prototype and so reachable over RPC by code holding a Host
  stub. `__facet` now refuses a killed object, but whether a live facet can
  be reached around admission this way is not checked here.
