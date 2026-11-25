---
kind: fix
upgrade: []
---
`tw deploy --wrangler` (and `tw dev --wrangler`) now runs wrangler with the
project's `node_modules/.bin` first on PATH, as `npm run deploy` does. A
wrangler `build.command` that calls a locally installed tool (for example
`esbuild …` in Cloudflare's `durable-chat-template`) used to fail with
`command not found`.

A Python Worker (`main: *.py` or the `python_workers` flag) is now refused
with `not_supported: Python Workers are not supported`, instead of
esbuild's "No loader is configured for .py files".

New: `make e2e-compat` deploys 30 of Cloudflare's own example projects,
unmodified, to a real host on a real tailnet and calls each one as its
README does (`e2e/compat/README.md`).
