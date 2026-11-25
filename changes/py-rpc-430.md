---
kind: feature
upgrade: []
---
Another workload can call a Python Worker's RPC methods over a service
binding (`await env.PY.highlight_code(...)`), on its `Default` or a named
`WorkerEntrypoint` class, as it calls a JavaScript `WorkerEntrypoint`'s.
`get_caller(self.ctx)` in the `python` template's `tailworks.py` names the
calling node. Cloudflare's `js-api-pygments` example now runs deployed.
A Python Worker's app can't have a module named `_tw_main.py` (tailworks
generates it).
