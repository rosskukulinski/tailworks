Last update: 2026-11-24 PT. A facet object that calls ctx.abort() is recreated on its next request or alarm (#395).

## What changed

- Root cause: the facet Host (`Host_<Class>`, `hostd/gen/js/platform.js`)
  cached the `ctx.facets.get("app")` stub for the Host's lifetime. After
  the facet called `ctx.abort()`, that stub failed with the abort reason
  forever (workerd marks the error `durableObjectReset`), so every later
  fetch, RPC and alarm failed until the host restarted.
- Fix: `hostFacet` caches only the Dynamic Worker (`LOADER.get`) and calls
  `ctx.facets.get()` per call. `get()` of a running facet returns that
  instance; after an abort it starts a new one. `__tw_kill` no longer
  needs to clear the stub: `hostFacet` refuses a killed Host before any
  `get()`, so kill semantics are unchanged.
- Plain-DO mode (`egress: "workload"`) already recovered: workerd resets
  the actor itself. The regression test covers it too.

## Tests

- `go test ./hostd/gen -run TestCtxAbortRecreates -count=1 -v`
  (new, `hostd/gen/ctxabort_test.go`, real workerd, no network): fetch,
  RPC and alarm after an abort during a request and after the response
  (the Agents lifecycle example's `setTimeout(() => ctx.abort())`), twice
  each, facet and plain; storage survives; RPC and fetch reach the same
  new instance; killed objects (including ones that aborted before) stay
  410 / refuse RPC. Facet subtests fail on master (500 with the abort
  reason), pass with the fix.
- `make lint`: pass.
- `make test` (mbp slot): pass.
- `make e2e-compat-dev`: pass (20 projects, 1 XFAIL ag-playground #391).
- With `origin/compat-325-remaining` (#401) merged in temporarily:
  `TW_COMPAT_ONLY='^ag-next-lifecycle$' make e2e-compat-dev` reports
  XPASS, so #401's `ExpectProbeFail` for `ag-next-lifecycle` should be
  removed once both land.

## Gaps

- The request that is running when the object aborts still fails (as on
  Cloudflare).
