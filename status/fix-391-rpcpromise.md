Last update: 2026-11-24 PT. Fix #391: `env.X.get(env.X.idFromName(n))` works inside a facet; the Agents playground passes.

# fix-391-rpcpromise

## Root cause

In tailworks, not workerd or the Agents SDK. Inside a facet-mode object
(the default), `env.X` is the platform's `NsProxy` entrypoint stub
(`hostd/gen/js/platform.js`), which has one method, `getByName`. Every
other property of an RPC stub is a remote call, so `env.X.idFromName(n)`
returned an `RpcPromise`. The runtime's `guardNamespace` then saw no
string `id.name` and passed it on to `env.X.get(id)`, an RPC call whose
argument is that `RpcPromise`: workerd refuses with `Could not serialize
object of type "RpcPromise"`.

agents 0.34's `getAgentByName` does `namespace.get(namespace.idFromName(name))`,
so any agent calling another agent from inside an object hit it
(`SupervisorAgent.createChild`). The issue's "smaller app passes" was not a
bundler difference; it presumably reached the child through `getByName` or
from the Worker, where `env.X` is a real namespace. `docs/egress-modes.md`
listed `idFromName`/`get(id)` as unavailable inside facets.

## Fix

- `js/packages/tailnet/src/facetns.ts` (new): `facetNamespaces` gives each
  facet `NsProxy` binding the namespace API before the create-admission
  guard wraps it. `idFromName(n)` is a local id carrying the name
  (`name`, `equals`, `toString()` = the name); `get(id)` routes by name to
  `getByName`. `idFromString`/`newUniqueId` throw `bad_request` (objects
  are addressed by name; before they failed later and obscurely),
  `jurisdiction` throws workerd's own "not implemented" error, as the
  Worker's real namespace does.
- `runtime.ts` `wrapFacetClass`: one call. `hostd/gen/js/tw-rt.js`
  regenerated (`make gen-js`).
- `e2e/compat/upstream.go`: the `ag-playground` `ExpectProbeFail` is gone;
  `docs/COMPAT.md` and `e2e/compat/upstream-results.json` regenerated
  (`make compat-upstream`) from the runs below; `e2e/compat/README.md`
  finding 1 and `docs/egress-modes.md` updated.

## Tests

- Regression: `go test ./hostd/gen -run TestFacetNamespaceIDs`
  (`hostd/gen/facetns_test.go`): the Worker, a facet object and a plain DO
  each reach a facet object and a plain DO by `get(idFromName)` and
  `getByName`. Before the fix the facet caller failed with the exact
  `RpcPromise` DataCloneError; now passes.
- `TW_COMPAT_ONLY='^ag-playground$' make e2e-compat-dev`: XFAIL before,
  PASS (26 steps) after.
- `TW_COMPAT_ONLY='^ag-playground$' make e2e-compat` (deployed, real
  tailnet): PASS (26 steps).
- `make e2e-compat-dev` (all projects), `make lint`, `make test` (mbp),
  `make templates-typecheck test-sdk`: see the PR.
