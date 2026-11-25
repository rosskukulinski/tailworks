# agent

A Cloudflare Agents SDK `Agent` on tailworks. Each agent instance is a Durable
Object with its own SQLite storage **and its own tailnet node**: callers
reach it by name, its `fetch()` leaves from that node, and `tw kill` cuts
it off.

It shows:
- state (`setState`) and SQL (`this.sql`) per instance;
- the verified caller in `onRequest` and in WebSocket `onConnect`;
- `schedule()` reminders (alarms);
- sub-agents with their own nodes and narrower capabilities
  (`tailnet(ctx).spawn(…, { caps, ttl })`).

## Run it

```sh
tw dev
curl http://127.0.0.1:8787/agents/support-agent/alice    # state.visits goes up on each call
curl http://127.0.0.1:8787/                              # your own agent, named after your login
curl 'http://127.0.0.1:8787/agents/support-agent/alice/remind?in=5&note=hi'
curl http://127.0.0.1:8787/agents/support-agent/alice/spawn   # a LookupAgent sub-agent
tw deploy
```

On a tailnet, `tw ls` lists every agent with its node, `tw audit <object>`
shows who created and called it, and `tw kill <object>` stops it
and its sub-agents.

## What to change

- `src/index.ts`: your agent classes. Add each class to `wrangler.jsonc`
  (`durable_objects`, `migrations`) and to `tailworks.jsonc` `tailnet.objects`
  with its tags.
- `tailworks.jsonc` `tailnet.objects.<Class>`: `"egress": "workload"` (plain-DO
  mode) if the class needs native alarms and doesn't need its own egress
  identity ([egress-modes.md](https://github.com/rosskukulinski/tailworks/blob/master/docs/egress-modes.md));
  `"ttl"` to end sub-agents.
- `tailworks.jsonc` `tailnet.spawn`: add `{ "maxLive": N, "maxDepth": N }` to
  lower the sub-agent budget below the defaults (64 live sub-agents under one
  root, depth 3). A deployment can only lower them.
- The `example.com/cap/mcp` capability in `/spawn` and `/delegate`: what a
  sub-agent may do, narrowed from your grants. Pair it with the `mcp`
  template.
