---
kind: fix
upgrade: []
---
docs/python.md no longer says only pure-Python packages work: packages
pywrangler vendors run, including Pyodide builds of packages with native code
(pydantic-core under FastAPI, Pillow). The compat suite now vendors the Python
examples' packages with `uv run pywrangler sync` and runs Django, FastAPI,
Flask, the Python MCP SDK, Pillow and requests/httpx examples. RPC into a
Python Worker over a service binding is documented as not supported yet (#430).
