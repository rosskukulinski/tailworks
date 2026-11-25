---
kind: feature
upgrade: [cli-json]
---
**Typed templates and `tw types`.** Every `tw init` template has a `tsconfig.json` and `@cloudflare/workers-types` and `typescript` dev dependencies, so `npm install && npx tsc --noEmit` passes and editors know `getCaller`, `Request`, `DurableObjectState` and `ctx.tailnet` (`@tailworks/tailnet` now declares `ctx.tailnet` on `DurableObjectState` and `ExecutionContext`). New `tw types`, like `wrangler types`, writes `worker-configuration.d.ts` with the app's `Env` (vars, Durable Object, KV, R2, D1, Queue and Workflow bindings, secret names from `.dev.vars`, `.dev.vars.example`, `secrets.required` or `--remote`); `tw types --check` fails with `types_stale` in CI. The `@tailworks/*` packages and `create-tailworks` are ready to publish to npm, and `npm create tailworks` runs `tw init` when tailworks is installed.
