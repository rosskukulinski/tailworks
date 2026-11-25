---
kind: feature
upgrade: [cli-json]
---
Testing tailworks apps (docs/testing.md). `@tailworks/tailnet/testing` adds
`withCaller` / `withPublic` and a fake `ctx.tailnet` for unit tests, in Node
or under `@cloudflare/vitest-plugin`, which runs tailworks apps' Worker and Durable
Object logic as on Cloudflare. `tw dev --caller <name>[=<tag>,…]` adds extra
dev identities, each with its own local URL, listed as `callers` in the
`ready` event and in `tw dev --once --json`; `@tailworks/tailnet/testing/dev`
(`startDev`) runs an app under `tw dev` for black-box tests. Every template
now has a test and an `npm test` script. `tw dev` no longer misses an edit
made right after its `ready` event.
