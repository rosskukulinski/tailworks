---
kind: breaking
upgrade: []
---
`ctx.tailnet.self()` now answers `workload` (the workload's name) and
`deploymentId` too, and the `refund-approval` template reads its
`tw approve <app> <id>` hint from it: its wrangler.jsonc has no
`vars.APP_NAME` any more, and `tw init` no longer rewrites that var.

`@tailworks/tailnet` drops the parts of on-behalf-of (RFC §4.6) that were
never built, so tool servers can't trust fields that carry no verified data:
`Caller.actingFor` (always `undefined`), `Caller.chain` (always the caller
alone) and `ctx.tailnet.forward()` (a `fetch` with the headers copied) are
gone from the types. Migration: use `caller.nodeId` / `caller.tags` instead
of `chain[0]`, and replace `ctx.tailnet.forward(request, url)` with
`ctx.tailnet.fetch(new Request(url, request))`. Already deployed apps keep
working: the host runtime still answers `forward()` for one more minor
release. In `@tailworks/mcp`, error details no longer carry `chain`, and a
grant that limits a tool to `groups` still denies it (no caller can show a
group until on-behalf-of is built).

The `node-workflow` template no longer sets `node.health`: any HTTP answer
on `/` already counts as up.
