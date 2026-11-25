# @tailworks/tailnet

The tailnet side of a [tailworks](https://github.com/rosskukulinski/tailworks)
Worker or Durable Object: who is calling (verified over WireGuard), and
egress from the workload's or the object's own tailnet node.

```ts
import { getCaller, tailnet, isTailnetError } from "@tailworks/tailnet";

export default {
  async fetch(req: Request, env: Env, ctx: ExecutionContext) {
    const who = getCaller(req); // nodeId, node, tags, user, caps()
    return Response.json({ hello: who.node });
  },
};
```

- `getCaller(req)`, `getRequester(req)`, `principalKey(caller)`: the
  verified caller of a request.
- `tailnet(ctx)`: `ctx.tailnet` (`fetch`, `spawn`, `kill`, `self`,
  `audit`), or a typed `no_identity` error off tailworks.
- `TailnetError`, `isTailnetError`, `fromResponse`: tailworks' typed errors
  and their catalog.
- `@tailworks/tailnet/testing`: `withCaller` and a fake `ctx.tailnet` for
  unit tests; `@tailworks/tailnet/testing/dev`: black-box tests against
  `tw dev`. `testing/dev` only drives the `tw dev` process, so it works for
  any runtime: that is why the Node-tier templates list `@tailworks/tailnet`
  as a devDependency.

Add `"@tailworks/tailnet"` to `compilerOptions.types` in `tsconfig.json`
(next to `@cloudflare/workers-types`) to type `ctx.tailnet`. Every `tw init`
template does this.

Docs: [testing](https://github.com/rosskukulinski/tailworks/blob/master/docs/testing.md),
[errors](https://github.com/rosskukulinski/tailworks/blob/master/docs/errors.md),
[wrangler compatibility](https://github.com/rosskukulinski/tailworks/blob/master/docs/wrangler.md).
