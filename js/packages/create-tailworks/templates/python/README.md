# python

A Python Worker (Pyodide, as on Cloudflare): it answers with its **verified
tailnet caller** and counts each caller's visits in a Python Durable Object.

## Run it

```sh
tw dev                                # http://127.0.0.1:8787, no tailnet needed
curl http://127.0.0.1:8787/            # {"hello":"<you>","greeting":"hello","visits":1,"caller":{…}}
tw deploy                             # then curl the URL it prints, from any device on your tailnet
```

The first run fetches the pinned Pyodide bundles once (about 50 MB); later
runs start in a second or two.

## What to change

- `src/entry.py`: your handler. `get_caller(request)` (in `src/tailworks.py`)
  tells you who is calling (node, user, tags, capabilities); there are no
  tokens to check.
- Packages: add them to a `pyproject.toml`, run `uv run pywrangler sync` to
  vendor them into `python_modules/`, and `tw deploy` ships them (pywrangler
  picks Pyodide builds of packages with native code).
- `tailworks.jsonc` `tailnet.tags`: the workload node's tag, named after your
  app by `tw init`.

The Durable Object has no tailnet node of its own (Python objects are
internal). Cron triggers, Queues and static assets work as in a JavaScript
Worker; Workflows aren't supported for Python yet. More:
[docs/python.md](https://github.com/rosskukulinski/tailworks/blob/master/docs/python.md).
