# py-rpc-430: RPC into a Python Worker over a service binding (#430)

## What changed

- `runtime.ts` `bindingDispatcher.call`: when the main module's default
  export has `Symbol.for("tailworks.pyRpc")` (the Python front), the call
  goes there after the caller seal and the handler-name / `constructor`
  refusals. `fetch()` to a named entrypoint of a Python Worker is a clear
  `service_not_found` ("answers RPC calls, not fetch()").
- `hostd/gen/pyrpc.go`: tenant-py's main module is generated
  (`_tw_main.py`): it re-exports the app's main module and adds
  `TailworksRpc`, which the front calls over a front-only binding
  (`__TW_PY_RPC`). `call()` resolves `Default` or a named
  `WorkerEntrypoint` class, allows only public functions that aren't
  handler names (what workerd's introspection offers), sets `ctx.cf` to
  the call's sealed cf (no ticket) and runs the method. Naming
  `TailworksRpc` as an entrypoint is refused.
- `tailworks.py` (python template): `get_caller(self.ctx)` works in an
  RPC method.
- e2e/compat: `Project.Companions` (Workers deployed first, under their
  own IDs) and `ExpectDev`; `py-js-api-pygments` replaces the not-run
  row; the compat policy gains a `tag:tw-wl-compat` → `tag:tw-wl-compat`
  tcp:80 grant (a service binding is a tailnet call).
- Docs: python.md (RPC section, Limits), COMPAT.md python row and the
  generated Python table, wrangler.md, ADR-055 decision 10.

## Results

- `hostd:TestPythonRPC` (new): args/returns, a Python exception reaches
  the caller (`ValueError: boom: from js`), `get_caller(self.ctx)` names
  the calling workload node, named entrypoint, refusals (unknown method,
  `_private`, handler name, `TailworksRpc` by name, fetch to a named
  entrypoint). Runs under the host sandbox when there is one: passes on
  macOS (Seatbelt) and on Lima twd-dev (Landlock), with
  `TestPythonWorkerSandboxed`.
- `hostd/gen` Python tests pass with the generated main module, plus
  `TestGeneratePythonReservedMain`.
- `TW_COMPAT_ONLY='^(py-|c3-python$)' make e2e-compat`: all pass
  (py-js-api-pygments PASS deployed; py-workflows and py-mcp-server XFAIL
  as before).
- Same selection with `make e2e-compat-dev`: 16 PASS, 2 XFAIL
  (py-js-api-pygments: tw dev has no binding to another workload).

## Gaps

- js-api-pygments under `tw dev`: `tw dev` runs one Worker and has no
  service binding to another workload (an existing limit, not Python's).
- `fetch()` to a named entrypoint of a Python Worker is refused (RPC to it
  works).
