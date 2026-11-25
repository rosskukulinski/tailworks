---
kind: breaking
upgrade: []
---
`tw deploy` and `tw dev` now refuse an app whose own code imports
`cloudflare:email`, with `not_supported`: "cloudflare:email is not
supported on tailworks yet (imported by <file>)". Before, the import
worked and only `new EmailMessage()` threw at run time. Email isn't
supported yet, so remove the import, or keep it out of the tailworks
build. "Own code" is any file outside `node_modules`, including workspace
and linked packages. A dependency's import under `node_modules` (the
Agents SDK imports `cloudflare:email` in its main module) still gets the
stub, as do pre-built (`no_bundle`, Vite-built) and `--wrangler` Workers,
where tailworks can't tell whose import it is.
