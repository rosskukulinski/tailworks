# node-workflow

A durable Vercel Workflow (`"use workflow"`) on tailworks' **Node process
tier** (`"runtime": "node"`): one supervised Node.js process with one
tailnet node. A support workflow uses an AI SDK `WorkflowAgent` (a mock
model) that calls tailworks MCP tools over the tailnet, sleeps, then waits for a
human approval hook. Kill the process at any point: tw-hostd restarts it
and the run resumes from its event log.

## Run it

Needs Node.js 24 or later and npm on this machine; `npm install` fetches
the template's dependencies, so this one can't be built offline.

```sh
npm install
tw dev
curl -X POST -d '{"order":"o1","usd":5,"mcp":"http://tw-mcp/mcp"}' http://127.0.0.1:8787/refunds
#   {"runId":"…","approveToken":"approve:o1"}
curl http://127.0.0.1:8787/runs/<runId>
curl -X POST http://127.0.0.1:8787/hooks/approve:o1   # resume the waiting run
tw deploy
```

The tools need an MCP server (the `mcp` template) at the `mcp` URL you
pass.

## What to change

- `workflows/support.ts`: your workflow and its steps.
- `workflows/mock-llm.ts`: replace with a real model, reached through a
  gateway on your tailnet.
- `src/server.ts`: your routes. `requireCaller(req)` verifies the tailnet
  caller; `enforceEgress()` sends raw connections through tailworks' egress
  proxy.
- `tailworks.jsonc` `tailnet.connects`: the MCP server's tag, so `tw deploy`
  checks the grant.

More: [node-tier.md](https://github.com/rosskukulinski/tailworks/blob/master/docs/node-tier.md).
