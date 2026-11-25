---
kind: feature
upgrade: []
---
Python Workers (Pyodide) run on tailworks (#292): a wrangler project with
`main = "src/entry.py"` and the `python_workers` flag deploys with
`tw deploy` and runs under `tw dev`, with Python Durable Objects (storage,
RPC, WebSocket hibernation), KV, R2, D1, service bindings and pure-Python
packages vendored into `python_modules/` by pywrangler. The caller's
verified tailnet identity is in `request.cf`, as for JavaScript Workers. New
template: `tw init --template python`. Cloudflare's own Python examples are
in the compat suite and COMPAT.md. Not yet supported for Python: crons,
Queues, Workflows, static assets, containers and the microVM tier; Python
Durable Objects have no tailnet node of their own. See docs/python.md.

Operator action: the first Python deployment on a host fetches the pinned
Pyodide bundles (about 53 MB, digest-checked) into `<data-dir>/pyodide`
(`tw-hostd -pyodide-dir`). On a host without internet access, copy them
there first (docs/python.md#offline-hosts).
