# pi-agent

Cloudflare's [Pi harness example](https://developers.cloudflare.com/agents/harnesses/pi/)
on tailworks: `PiHarness` from `agents/harness/pi` runs Pi Durable
(`@earendil-works/pi-durable`) inside a Durable Object. Pi keeps the
transcript, inbox, model calls, tool calls, retries and crash recovery in
the object's SQLite (`pi_*` tables), and a Lifecycle job on the object's
alarm wakes it, so a run survives eviction. It has the example's
`word_count` tool and "editor" system prompt section.

Each assistant is a Durable Object and its own tailnet node; `GET /` is the
caller's own assistant, keyed by their tailnet identity.

## Run it

```sh
tw deploy                       # or `tw dev` for a local run with no tailnet
curl 'http://<workload node>/?prompt=What+is+47+%C3%97+19%3F'
curl 'http://<workload node>/?prompt=Count+the+words+in+%22the+quick+brown+fox%22'
curl 'http://<workload node>/'                  # your transcript
curl 'http://<workload node>/agents/demo?prompt=hi'   # a named assistant
```

## The model: Aperture, or a mock

With `vars.LLM_URL` empty (the default) the model is a **scripted mock**
(pi-ai's faux provider, `src/model.ts`) that needs no provider and no keys.
Cloudflare's version uses Workers AI (`env.AI`), which tailworks doesn't
have.

To use [Aperture](https://tailscale.com/docs/aperture/what-is-aperture),
Tailscale's AI gateway on your tailnet:

1. In `wrangler.jsonc`, set `vars.LLM_URL` to `http://<aperture-hostname>/v1`
   and `vars.LLM_MODEL` to a model Aperture serves.
2. In `tailworks.jsonc`, set `tailnet.connects` to Aperture's tag, so
   `tw policy` prints the grant from the assistants' tag to Aperture and
   `tw deploy` checks it.
3. In Aperture's configuration, grant the assistants' tag
   (`tag:tw-pi-agent-…`, from `tw policy`) the models they may use.
   Aperture is deny-by-default.

`src/model.ts` builds a pi-ai provider on Pi's `openai-completions` API for
it. The app holds no API key: Pi's requests leave from the assistant's own
node, Aperture identifies it by that node and adds the provider's key, and
its logs, usage and quotas are per assistant. Use a shared-key provider in
Aperture (the provider sends a placeholder key, which passthrough mode
would forward).

## What to change

- `src/index.ts`: the tools and prompt sections (`registry.install`), and
  `defaults` (`model`, `maxTurns`). See Cloudflare's page for sessions,
  steering, signals and `beforeTool`.

Pi needs `agents` 0.34.0 or later. Its packages are pinned to 1.6.0: an
agent that has run on Pi 1.6 can't go back to 1.5.
