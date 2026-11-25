Last update: 2026-11-24 15:45 PT. Python Workers (Pyodide) on the process tier: Worker, Python Durable Objects, KV/R2/D1, vendored packages, `python` template, Cloudflare's Python examples in the compat suite (#292).

# python-support-292

Issue #292: run Cloudflare Python Workers (`main = "src/entry.py"`,
`compatibility_flags = ["python_workers"]`) with `tw deploy` and `tw dev`.
Design: `docs/design/adr/055-python-workers.md` (ADR-055, drafted as
ADR-python-workers). User doc: `docs/python.md`.

## What's built

- **Wrapper (hostd/gen/python.go).** workerd can't import a `.py` module
  from JavaScript, so a Python app gets a second tenant worker, `tenant-py`,
  with the Python modules and the app's bindings. The `tenant` worker becomes
  a generated JS front (`__tw_py.js`) that forwards to it over a service
  binding; `__tw_main.js` wraps the front as any JS Worker, so the caller
  seal, the public/tailnet split and tracing are unchanged. Only the front
  can reach `tenant-py`.
- **Bundle (hostd/bundle/python.go).** `App.Python`; module type `py`;
  Python DO classes forced to plain-DO internal (`node: false`); refused by
  name: crons, queues, workflows, containers, assets, `tier: microvm`,
  tags/agent/ttl/public on a Python class, a missing `python_workers` flag.
- **Pyodide (internal/pyodide, hostd/pyodide.go).** The three bundles the
  pinned workerd uses, pinned by size + SHA-256. hostd fetches missing ones
  once into `-pyodide-dir` (default `<data-dir>/pyodide`; `tw dev`: the
  per-user cache), and passes `--pyodide-bundle-disk-cache-dir`; the sandbox
  gets that directory read-only, nothing else. `python_unavailable` on a
  fetch/digest failure. A Python app resolving to the microVM tier (tenant
  default) is refused with `tier_unavailable`.
- **CLI (cmd/tw/python.go).** No bundler: main's directory (`.py` as Python
  modules, other files as data) + `python_modules/` under `dist/`. Refuses
  `--wrangler`; errors when the date needs the external `workers` SDK
  (≥ 2026-04-21, measured) and it isn't vendored; warns when
  `pyproject.toml`/`requirements.txt` declare packages and nothing is
  vendored.
- **Template** `tw init --template python`: Worker + Python DO counter,
  `src/tailworks.py` with `get_caller(request)` (getCaller's fields).
- **Compat suite (e2e/compat/python.go).** New COMPAT.md section
  "Upstream examples: Python Workers": all 28 examples of
  cloudflare/python-workers-examples@47ec7d69f405 accounted for. `c3-python`
  flipped from "refused" to "pass". `py-query-d1` runs deployed only (D1
  seeding).

## Findings

- workerd picks the Pyodide bundle by compat date: 2024 dates → 0.26.0a2,
  2025-2026 → 0.28.2 (Python 3.13), from about 2026-09 → 314.0.6 (3.14).
- From compatibility_date 2026-04-21 the `workers` SDK isn't built in
  (`workers-runtime-sdk`, vendored by pywrangler), unless
  `disable_python_external_sdk` (bisected on the pinned binary).
- workerd runs from a read-only bundle dir with no network (checked under a
  hand-written Seatbelt profile, then through hostd's own profiles).
- `request.cf` crosses the front → `tenant-py` service binding intact.
- An ES main importing a `pythonModule` fails to start ("Module … already
  added to bundle").
- pywrangler ships non-`.py` files next to main (the chatroom example reads
  `chatroom.html` from disk), so the packer ships them too, as data.

## Tests run (all on this branch)

- `go test ./hostd/gen -run Python` (TestPythonWorker, TestPythonCallerUnsealed,
  TestGeneratePython): pass, macOS arm64 and Linux arm64 (Lima twd-dev).
- `go test ./hostd -run TestPython` (TestPythonWorkerSandboxed under
  Seatbelt on macOS and Landlock on Linux arm64; TestPythonMicroVMRefused): pass.
- `go test ./cmd/tw -run 'TestDevPython|TestIsPythonApp|TestImportsWorkersSDK|TestDeclaresPackages'`: pass (macOS; TestDevPython also on Linux).
- `go test ./hostd/bundle -run TestParsePython`, `go test ./internal/pyodide`: pass.
- `TW_COMPAT_ONLY='^(py-|c3-python$)' make e2e-compat-dev`: 6 PASS, 4 XFAIL.
- `TW_COMPAT_ONLY='^(py-|c3-python$)' make e2e-compat` (tailnet 1, real
  tw-hostd): 7 PASS (incl. py-query-d1), 4 XFAIL. Recorded with
  `make compat-upstream`.
- `make lint`: pass. `make test-js`: pass. `sh scripts/templates-typecheck.sh python`: skips it (no TypeScript).
- `make test` (test-go under load ~28): everything passed except
  `mgmt:TestWorkloadReplicas`, `mgmt:TestWarmKeyRefillRespectsThrottle` and
  `hostd:TestContainerPolyfill`, timing-sensitive tests in code this branch
  doesn't touch; all three pass rerun alone (`go test -count=1 -run …`).

## Gaps / next

- Python DOs with their own node (facet mode), `ctx.tailnet` for Python.
- Python crons/queues (front could forward `scheduled()`/`queue()` with
  `service_binding_extra_handlers`), Workflows, assets.
- microVM tier: bake the bundles into the guest image, pass the flag.
- Memory snapshots (cold start ≈ 1 s on a laptop).
- The suite doesn't run uv, so examples needing vendored packages are
  "not run".
