# chat-agent

Cloudflare's [chat agent example](https://developers.cloudflare.com/agents/examples/chat-agent/)
on tailworks: an `AIChatAgent` from `@cloudflare/ai-chat` with a chat page.
It streams replies, persists messages in SQLite, resumes streams on
reconnect and hibernates when idle. It has the example's three kinds of
tool:

| Tool | Runs | Behavior |
| --- | --- | --- |
| `getWeather` | on the server | automatically |
| `getUserTimezone` | in the browser | no `execute`; the page answers it |
| `calculate` | on the server | asks for approval first when a number is over 1000 |

Each chat is a Durable Object and its own tailnet node, so what it calls
(the model, a weather API) sees that chat, not the whole app. `/` is the
caller's own chat, keyed by their tailnet identity.

## Run it

```sh
tw dev                          # then open http://127.0.0.1:8787/
tw deploy                       # then open http://<workload node>/ from your tailnet
```

The page at `/` is the chat UI. It speaks the same WebSocket protocol as
`useAgentChat` (`src/page.ts` lists the messages), so you can swap it for
Cloudflare's React client (`useAgent` from `agents/react`, `useAgentChat`
from `@cloudflare/ai-chat/react`) pointed at `/agents/chat-agent/<name>`.

Over HTTP, for scripts and tests:

```sh
curl -d 'What is the weather in Tokyo?' http://<workload node>/
curl http://<workload node>/messages            # your transcript
```

## The model: Aperture, or a mock

With `vars.LLM_URL` empty (the default) the model is a **scripted mock**
(`src/model.ts`) that needs no provider and no keys. Cloudflare's version
uses Workers AI (`env.AI`), which tailworks doesn't have.

To use [Aperture](https://tailscale.com/docs/aperture/what-is-aperture),
Tailscale's AI gateway on your tailnet:

1. In `wrangler.jsonc`, set `vars.LLM_URL` to `http://<aperture-hostname>/v1`
   and `vars.LLM_MODEL` to a model Aperture serves.
2. In `tailworks.jsonc`, set `tailnet.connects` to Aperture's tag, so
   `tw policy` prints the grant from the chats' tag to Aperture and
   `tw deploy` checks it.
3. In Aperture's configuration, grant the chats' tag
   (`tag:tw-chat-agent-…`, from `tw policy`) the models they may use.
   Aperture is deny-by-default.

The app holds no API key: each chat's requests leave from its own node,
Aperture identifies it by that node and adds the provider's key, and its
logs, usage and quotas are per chat. Use a shared-key provider in
Aperture; in passthrough mode it would forward the client's (empty)
credentials. Any other OpenAI-compatible gateway on your tailnet works the
same way; if it needs a key, keep it a secret (`tw secret put LLM_API_KEY`)
and pass it in `src/model.ts`.

## What to change

- `src/index.ts`: the tools, the system prompt, `stopWhen`.
- `src/page.ts`: the chat page.
