---
kind: fix
upgrade: []
---
A Durable Object that calls `ctx.abort()` now gets a fresh instance on its
next request or alarm, with its storage and alarm intact, as on Cloudflare
and under `wrangler dev` (#395). Before, an object running in facet mode
(the default) never recovered: every later request failed with the abort
reason until the host restarted. Only the request that was running when
the object aborted fails. `tw kill` is unchanged: a killed object stays
dead.
