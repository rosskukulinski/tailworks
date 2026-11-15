# agent

A Cloudflare Agents SDK `Agent` on twd. Each agent instance is a Durable
Object with its own SQLite storage **and its own tailnet node**: callers
reach it by name, its `fetch()` leaves from that node, and `twd kill` cuts
it off.

It shows:
- state (`setState`) and SQL (`this.sql`) per instance;
- the verified caller in `onRequest` and in WebSocket `onConnect`;
- `schedule()` reminders (alarms);
- sub-agents with their own nodes and narrower capabilities
  (`tailnet(ctx).spawn(…, { caps, ttl })`).

## Run it

```sh
twd dev
curl http://127.0.0.1:8787/agents/support-agent/alice    # state.visits goes up on each call
curl http://127.0.0.1:8787/                              # your own agent, named after your login
curl 'http://127.0.0.1:8787/agents/support-agent/alice/remind?in=5&note=hi'
curl http://127.0.0.1:8787/agents/support-agent/alice/spawn   # a LookupAgent sub-agent
twd deploy
```

On a tailnet, `twd ls` lists every agent with its node, `twd audit <object>`
shows who created and called it, and `twd kill <object> --cascade` stops it
and its sub-agents.

## What to change

- `src/index.ts`: your agent classes. Add each class to `wrangler.jsonc`
  (`durable_objects`, `migrations`) and to `twd.jsonc` `tailnet.objects`
  with its tags.
- `twd.jsonc` `tailnet.objects.<Class>`: `"egress": "workload"` (plain-DO
  mode) if the class needs native alarms and doesn't need its own egress
  identity ([egress-modes.md](https://github.com/rosskukulinski/twd/blob/master/docs/egress-modes.md));
  `"ttl"` to end sub-agents.
- `twd.jsonc` `tailnet.spawn`: how many sub-agents may live at once, and how
  deep they may nest.
- The `example.com/cap/mcp` capability in `/spawn` and `/delegate`: what a
  sub-agent may do, narrowed from your grants. Pair it with the `mcp`
  template.
