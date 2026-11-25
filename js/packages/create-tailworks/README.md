# create-tailworks

Create a [tailworks](https://github.com/rosskukulinski/tailworks) app from a
template:

```sh
npm create tailworks@latest my-app -- --template hello
cd my-app && npm install && npx tsc --noEmit
```

With the `tw` CLI on your PATH this runs `tw init`; otherwise it copies
the template and renames it the way `tw init` does: the workload, the npm
package and the template's `tag:tw-…` tags are named after the directory
(or `--name`). `--tags` and `--object-tags` are passed to `tw init` and
need it. The templates are in [`templates/`](templates/):

- `hello`: a Worker that returns its verified caller and does a private fetch.
- `agent`: a Cloudflare Agents SDK `Agent` (state, `this.sql`, per-object node
  and egress, `routeAgentRequest` with WebSockets and `getCaller` in
  `onConnect`, `schedule()`, `ctx.tailnet.spawn` of a sub-agent).
- `mcp`: a stateless MCP server (`createMcpHandler` from `agents/mcp/server`)
  wrapped in `withTailnet`, with `authorizeTool`, a DO-backed `budget` and a
  `whoami` tool.
- `ai-agent`: a Vercel AI SDK `ToolLoopAgent` in a Durable Object (one agent,
  one node per caller) whose tools are a tailworks MCP server's tools over the
  tailnet (`@ai-sdk/mcp` + `@tailworks/mcp` `mcpFetch`); it streams a UI message
  response. It ships with a scripted mock model (`src/model.ts`, no keys);
  swap in a real provider. Its tools need a second app, the `mcp` template
  deployed as workload `mcp`, and a grant; its README walks through both.
- `lock`: a lease-based lock and leader-election service: one Durable
  Object per lock name, holders recorded by tailnet identity, fencing tokens
  that keep increasing across a move to another host, and a copyable client
  (`src/client.ts`: `acquire`, `renew`, `release`, `withLock`). See
  [docs/locks.md](https://github.com/rosskukulinski/tailworks/blob/master/docs/locks.md).
- `static-site`: wrangler `assets`: the files in `public/` served next to a
  Worker that answers `/api/*` and checks the caller (`getCaller`) before it
  serves `/team/`. No build step. See
  [docs/wrangler.md](https://github.com/rosskukulinski/tailworks/blob/master/docs/wrangler.md#assets).
- `refund-approval`: a Cloudflare Workflow (`AgentWorkflow`) started by an
  Agent that waits for a person's approval (`waitForApproval`). Tailworks delivers
  the approval only from a tailnet identity holding `github.com/rosskukulinski/tailworks/cap/approve`
  (`tw approve`, the admin UI, or the page at `/`), and the Workflow sees
  who approved; it also refuses an approval by the requester. See
  [docs/approvals.md](https://github.com/rosskukulinski/tailworks/blob/master/docs/approvals.md).
- `node-workflow`: the opt-in Node process tier
  ([docs/node-tier.md](https://github.com/rosskukulinski/tailworks/blob/master/docs/node-tier.md)):
  a `node:http` server with a Vercel Workflow (`"use workflow"`) behind
  `@tailworks/node`. Needs Node.js and `npm install` on the deploying machine; it is
  not deployable offline from the release.

The verified SDK surface and its gaps are in
[docs/COMPAT.md](https://github.com/rosskukulinski/tailworks/blob/master/docs/COMPAT.md).

Each template has a `tsconfig.json` (`@cloudflare/workers-types` plus
`@tailworks/tailnet`) and a `worker-configuration.d.ts` with the global
`Env` that `tw types` generates from `wrangler.jsonc` and `tailworks.jsonc`.
Run `tw types` again after you change either.

`create-tailworks build [dir]` and `create-tailworks build-node [dir]` are
the bundlers `tw deploy` uses.
