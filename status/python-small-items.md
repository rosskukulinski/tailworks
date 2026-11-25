Last update: 2026-11-24 19:30 PT. Python follow-ups: the compat suite vendors packages with uv (10 more Cloudflare examples run), an amd64 microVM guest run, and the docs' package claims corrected.

# python-small-items

Continues `status/python-microvm.md`.

## What changed

- `e2e/compat/python.go`: `uvSync` build step (`uv run pywrangler sync`)
  for the examples that need packages. New published projects: py-fastapi,
  py-django, py-django-markdown-r2, py-django-todo-d1, py-mcp-server,
  py-image-gen, py-sync-http-clients. py-url-shortener now passes (the
  vendored workers SDK) instead of being an expected refusal. Deployed-only:
  py-fastapi-todo, py-flask-todo (seeded with `d1 execute`).
- `e2e/compat/dev_test.go` `devRunsSetup`: a project whose only post-deploy
  steps are `d1 migrations apply` runs under `tw dev` too (tw dev applies
  migrations itself).
- py-mcp-server is an expected failure **deployed**: the example keeps the
  Python MCP SDK's default host 127.0.0.1, which turns on DNS-rebinding
  protection allowing only localhost Host headers (421 by any deployed name).
- `js-api-pygments` stays not run: RPC into a Python Worker isn't supported
  (#430, filed).
- `.github/workflows/nightly.yml`: setup-uv for e2e-compat-dev.
- docs/python.md: compiled packages work when vendored; RPC limit; issue
  links (#424-#426, #430).

## Findings

- pywrangler resolves for Pyodide, so `pydantic_core` and Pillow ship as
  wasm32-emscripten `.so` files in python_modules/, and workerd loads them.
- Of the 28 examples, all but 4 now run somewhere (Workers AI x4,
  Hyperdrive, Worker Loader, the Bluesky firehose, the test harness, RPC are
  the rest).

## Tests run

- `TW_COMPAT_ONLY='^(py-|c3-python$)' make e2e-compat-dev`, `make e2e-compat`: see the PR.
- amd64 microVM guest: `make e2e-vm` on nas-1 twd-amd64 (Firecracker, nested; amd64 image built there, 261 MB): the whole suite passes (31 tests), incl. TestE2EVMPython and TestE2EVMPythonSnapshot (deploy → active ~9 s, first request ~140 ms).

## Not done

- mini's disk guard (`com.ross.disk-guard`): its StartInterval timer never
  fired after load, so the Go cache grew to 371 GB. A fix (hourly
  StartCalendarInterval, age-based trim before a full clean) needs Ross's
  go-ahead; it is outside this repo.
