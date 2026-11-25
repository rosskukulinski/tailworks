---
name: tw-cron
description: Use when a tailworks app needs scheduled work - Cloudflare-style cron triggers (wrangler.jsonc triggers.crons and a scheduled() handler) - or when testing a cron trigger locally with `tw dev --test-scheduled`.
---

# Cron triggers on tailworks

Declare the schedule in `wrangler.jsonc` and handle it in `scheduled()`,
as on Cloudflare:

```jsonc
// wrangler.jsonc
{
  "triggers": { "crons": ["*/5 * * * *"] }
}
```

```ts
export default {
  async fetch(req: Request, env: Env) { /* ... */ },
  async scheduled(controller: ScheduledController, env: Env, ctx: ExecutionContext) {
    // controller.cron: the expression; controller.scheduledTime: the minute (epoch ms)
  },
};
```

- Cloudflare's syntax: five fields, **UTC**, minute granularity. Day of
  week is **1-7 with 1 = Sunday** (`SUN`-`SAT` also work).
- A cron run has **no caller**: don't call `getCaller()` in
  `scheduled()`. Its outbound `fetch()` leaves from the workload's node, so
  the workload's grants decide what it can reach.
- Delivery is best effort: at most once per minute per trigger, missed
  minutes aren't backfilled, an overlapping run is skipped, no retries.
- Crons don't run on the Node.js runtime (`"runtime": "node"`).
- To remove every trigger, set `"crons": []` and redeploy.

## Test it locally

```sh
tw dev --test-scheduled        # long-running; see tw-dev
curl 'http://127.0.0.1:8787/__scheduled?cron=*/5+*+*+*+*'
```

The route runs only declared triggers. With no `cron` parameter it runs
the first one.

## Deploy and watch it

```sh
tw deploy --json
tw logs <workload> -f          # each run, with its console output
```

Run outcomes and failures are logged by tailworks' host agent:
`tw logs --component hostd` on the tailworks host.

More: `tw docs show cron-triggers.md`.
