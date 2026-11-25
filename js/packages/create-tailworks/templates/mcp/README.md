# mcp

A stateless MCP server (streamable HTTP, `createMcpHandler`) whose tools
are authorized **per caller by a tailnet grant**. There is no OAuth and no
session: WireGuard identifies every request, and the caller's app
capability says which tools it may call and with what limits.

Tools: `whoami`, `lookup_order`, `refund` (checks `maxUsd` per call and
`perDayUsd` per caller, kept in a Durable Object, `TwBudget`).

## Run it

```sh
tw dev                 # serves MCP on http://127.0.0.1:8787/mcp (its / answers 404)
tw dev --once          # smoke test: sends an MCP initialize to /mcp
tw deploy
```

Connect a client to `http://<workload node>/mcp`; an MCP client in another
tailworks app calls it over the tailnet (the `agent` and `ai-agent` templates
do). To use it from claude.ai or ChatGPT, which need a public URL with
OAuth, see
[mcp-public.md](https://github.com/rosskukulinski/tailworks/blob/master/docs/mcp-public.md).

## What to change

- `src/index.ts`: register your tools. Call
  `authorizeTool(caller(), cap, {tool, …})` at the top of each one, and
  `budget(…).reserve(n)` for anything with a running total.
- The tailnet policy decides who gets which tools. A grant to this
  workload's tag carries the app capability:

  ```jsonc
  {"src": ["tag:tw-<app>-support-agent"], "dst": ["tag:tw-wl-<app>"], "ip": ["tcp:80"],
   "app": {"example.com/cap/mcp": [{"tools": ["lookup_order", "refund"],
           "limits": {"refund": {"maxUsd": 50, "perDayUsd": 200}}}]}}
  ```
- `wrangler.jsonc` `vars.MCP_CAP`: the capability name, if you'd rather not
  use `example.com/cap/mcp`.

More: [agents.md](https://github.com/rosskukulinski/tailworks/blob/master/docs/agents.md)
and [COMPAT.md](https://github.com/rosskukulinski/tailworks/blob/master/docs/COMPAT.md#mcp).
