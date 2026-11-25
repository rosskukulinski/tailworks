# ai-agent

A Vercel AI SDK `ToolLoopAgent` whose tools are a tailworks MCP server's tools,
called over the tailnet. The loop runs in a Durable Object, one per caller,
so each agent is its own tailnet node, and the MCP server authorizes (and
audits) that agent, not the whole app. The reply streams as an AI SDK UI
message stream (what `useChat` reads).

**This is a two-workload demo.** The agent's tools live in a second app made
from the `mcp` template, and a grant in your tailnet policy says which of
those tools the agent may call. On its own (and under `tw dev`, which runs
one app) the agent's only reply is the typed refusal `peer_not_visible`.

The model is a **scripted mock** (`src/model.ts`), so the template runs with
no provider and no keys. It **ignores the prompt**: every request calls
`whoami`, `lookup_order {"order":"o1"}` and `refund {"order":"o1","usd":10}`,
then replies with the three results. The grant decides which calls succeed.

## Run it

You need a running tailworks (`tw up` or `tw link`) and access to your
tailnet policy (admin console › Access controls). Both apps go in sibling
directories.

**1. Deploy the MCP server as workload `mcp`.** The name matters: the
agent's `vars.MCP_URL` is `http://tw-mcp/mcp` (its node is `tw-` plus the
workload name), and its `tailnet.connects` is `tag:tw-wl-mcp`.

```sh
tw init mcp --template mcp
cd mcp
tw policy          # paste what it prints into your tailnet policy (or: tw policy --apply)
tw deploy
cd ..
```

**2. Scaffold the agent.**

```sh
tw init ai-agent --template ai-agent
cd ai-agent
```

`tw init` names the tags after the app: `tag:tw-wl-ai-agent` for the Worker
and `tag:tw-ai-agent-ai-agent` for each agent (a different directory name
gives different tags; `tailworks.jsonc` has yours).

**3. Grant the agent its tools.** Add this to the `"grants"` list of your
tailnet policy. It is also the comment at the bottom of your
`tailworks.jsonc`, with your tags filled in.

```jsonc
{ "src": ["tag:tw-ai-agent-ai-agent", "tag:tw-wl-ai-agent"], "dst": ["tag:tw-wl-mcp"], "ip": ["tcp:80"],
  "app": { "example.com/cap/mcp": [{ "tools": ["lookup_order"] }] } }
```

`tw policy` writes the rest the agent needs (its tagOwners and a plain
`tcp:80` grant to `tag:tw-wl-mcp`), but not this capability: tailworks
doesn't know which tools you want the agent to have. Without it, `whoami`
still works and both other tools fail with `cap_missing`.

**4. Deploy and call it.**

```sh
tw policy          # paste what it prints (or: tw policy --apply)
tw deploy          # prints the workload's URL: http://tw-ai-agent….ts.net/
curl -N '<that URL>'
```

You can call it only if a grant lets you reach `tag:tw-wl-ai-agent` on
port 80; `tw policy` prints one for everyone on your tailnet.

### What a working reply looks like

The body is an SSE stream (`data: {…}` lines, ending `data: [DONE]`): a
`tool-input-available` and a `tool-output-available` event per tool call,
then `text-delta` events whose text joins to (JSON shortened here):

```text
Tool results:
whoami: [{"type":"text","text":"{\"nodeId\":\"…\",\"node\":\"tw-ai-agent-aiagent-….ts.net\",\"tags\":[\"tag:tw-ai-agent-ai-agent\"]}"}]
lookup_order: [{"type":"text","text":"{\"order\":\"o1\",\"total\":42,\"status\":\"shipped\"}"}]
refund: [{"type":"text","text":"{\"code\":\"cap_missing\",\"message\":\"tool \\\"refund\\\" not granted to …\",…}"}]
```

- `whoami` shows the MCP server saw the agent's own node, not the workload.
- `lookup_order` succeeded because the grant lists it.
- `refund` was refused because the grant doesn't. Add `"refund"` (and
  `"limits": {"refund": {"maxUsd": 50, "perDayUsd": 200}}`) to the grant
  and every request refunds $10, until `perDayUsd` is spent.

A `403` with `tw-error: peer_not_visible` means the agent can't see
`tw-mcp`: the `mcp` workload isn't deployed under that name, or the
`tcp:80` grant to `tag:tw-wl-mcp` is missing. `cap_missing` in every
tool's result means the capability grant in step 3 is missing.

### Routes

`GET /?prompt=…` is your own agent, `/agents/<name>?prompt=…` a named one,
and `/worker?prompt=…` runs the same loop in the Worker, with the
workload's identity (that is why the grant includes `tag:tw-wl-ai-agent`).
The scripted model ignores `prompt`; a real model reads it.

### Under `tw dev`

`tw dev` runs one app on its own local control plane, so there is no MCP
server to reach: every request returns `peer_not_visible`, which is what
`test/app.test.js` checks. Use it to work on the agent code; try the tools
with a deployment.

## What to change

- `src/model.ts`: swap the mock for a real model. Point an
  OpenAI-compatible provider at an LLM gateway on your tailnet, and pass
  `fetch: ctx.tailnet.fetch` so requests leave from the agent's node
  (the file has the code). Keep any API key in a secret
  (`tw secret put LLM_API_KEY`), not in `vars`.
- `wrangler.jsonc` `vars.MCP_URL`: your MCP server's address. A bare name
  matches the first label of a node in the agent's netmap (`tw-mcp`), so
  a different workload name, or a `tw up --hostname-prefix` other than
  `tw-`, needs a different URL. `tw deploy --var MCP_URL=…` overrides it
  for one deploy.
- `tailworks.jsonc` `tailnet.connects`: the MCP server's tag, so `tw deploy`
  checks the `tcp:80` grant. The capability in the grant (step 3) carries
  which tools the agent may call.

More: [recipes.md](https://github.com/rosskukulinski/tailworks/blob/master/docs/recipes.md#call-an-llm-through-a-gateway-on-your-tailnet).
