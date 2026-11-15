# ai-agent

A Vercel AI SDK `ToolLoopAgent` whose tools are a twd MCP server's tools,
called over the tailnet. The loop runs in a Durable Object, one per caller,
so each agent is its own tailnet node, and the MCP server authorizes (and
audits) that agent, not the whole app. The reply streams as an AI SDK UI
message stream (what `useChat` reads).

The model is a **scripted mock** (`src/model.ts`), so the template runs
with no provider and no keys.

## Run it

Deploy the `mcp` template first: this agent's tools live there.

```sh
twd deploy                                  # needs an MCP server at vars.MCP_URL (default http://mcp/mcp)
curl 'http://<workload node>/?prompt=Where+is+order+o1'
```

`GET /?prompt=…` is your own agent, `/agents/<name>?prompt=…` a named one,
and `/worker?prompt=…` runs the same loop in the Worker, with the
workload's identity. Under `twd dev` there is no MCP server to reach, so
the reply is the typed refusal (`peer_not_visible`).

## What to change

- `src/model.ts`: swap the mock for a real model. Point an
  OpenAI-compatible provider at an LLM gateway on your tailnet, and pass
  `fetch: ctx.tailnet.fetch` so requests leave from the agent's node
  (the file has the code). Keep any API key in a secret
  (`twd secret put LLM_API_KEY`), not in `vars`.
- `wrangler.jsonc` `vars.MCP_URL`: your MCP server's address.
- `twd.jsonc` `tailnet.connects`: the MCP server's tag, so `twd deploy`
  checks the grant. The grant carries which tools the agent may call.

More: [recipes.md](https://github.com/rosskukulinski/twd/blob/master/docs/recipes.md#call-an-llm-through-a-gateway-on-your-tailnet).
