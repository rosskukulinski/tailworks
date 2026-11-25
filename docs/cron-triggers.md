# Cron triggers

tailworks runs Cloudflare-style cron triggers: declare them in `wrangler.jsonc`, and
the host runs your Worker's `scheduled()` handler on that schedule.

```jsonc
// wrangler.jsonc
{
  "triggers": { "crons": ["*/5 * * * *", "0 18 * * friL"] },
}
```

```js
export default {
  async fetch(req, env, ctx) { /* ... */ },
  async scheduled(controller, env, ctx) {
    // controller.cron: the expression as written; controller.scheduledTime:
    // the scheduled minute (epoch ms). ctx.tailnet works as in fetch().
    await env.JOBS.getByName("nightly").run();
  },
};
```

A class-based default export (`class extends WorkerEntrypoint`) with a
`scheduled()` method works too. To remove all triggers, set `"crons": []` (or
remove `triggers`) and redeploy.

## Syntax

The syntax is Cloudflare's: five fields, evaluated in **UTC**, at minute
granularity.

| Field | Values | Characters |
|---|---|---|
| Minute | 0-59 | `* , - /` |
| Hour | 0-23 | `* , - /` |
| Day of month | 1-31 | `* , - / L W` |
| Month | 1-12, `JAN`-`DEC` | `* , - /` |
| Day of week | **1-7, 1 = Sunday**, `SUN`-`SAT` | `* , - / L #` |

- `L` (day of month) is the last day of the month; `LW` is its last weekday;
  `15W` is the weekday nearest the 15th, within the same month.
- `6L` / `friL` (day of week) is the last Friday of the month; `2#1` /
  `mon#1` is the first Monday; `L` alone is Saturday.
- When both day fields are restricted (neither starts with `*`), a day
  matches if **either** does, as in Vixie cron.
- Day-of-week numbering is 1-7 with 1 = Sunday, as on Cloudflare. That is
  **not** crontab's 0-6. `0` is rejected so that a copied crontab line fails
  at deploy, rather than running on the wrong day. Use names (`SUN`) to
  avoid the ambiguity.

The limits are 10 triggers per workload, no duplicates, and 128 bytes per
expression. A schedule that can never fire (e.g. `0 0 30 2 *`) is rejected.
Validation happens when you upload: `tw deploy` fails with `400 bad_bundle`
and names the bad expression. The host checks again before it runs anything.

The `node` runtime has no `scheduled()` handler, so it does not support cron
triggers. A Node bundle that declares them is rejected. Vercel-style path
crons for the Node runtime are on the roadmap (STATUS.md, Next steps).

## Delivery

Delivery is best effort. Like Cloudflare's, it is not exactly once.

- **Who runs them.** Only the host serving the workload's active deployment
  runs its triggers. That host must also have heartbeated mgmt successfully
  within the fence window (30 s). The heartbeat rule covers stateless
  workloads too, which otherwise keep serving through a partition: by then
  mgmt may have moved the workload, and the new host runs its triggers. So
  a partition doesn't run them twice. At worst it skips a few minutes.
- **At most once per minute** per trigger. If the wall clock steps
  backwards, minutes that already ran are not run again, even across a
  hostd restart: the host keeps the last minute it ran in its data dir. It
  ignores a stored minute more than 15 minutes ahead of its clock, because
  that means the clock was fast and has been corrected. If the clock steps
  forwards (or the host sleeps), the missed minutes are not backfilled;
  only the current minute runs.
- **No overlap.** If a trigger's previous run is still going when it comes
  due again, that run is skipped (and logged as `cron trigger skipped`),
  not queued. As on Cloudflare, a run lasts until `scheduled()` returns
  **and** the promises it passed to `ctx.waitUntil()` settle, so work left
  in `waitUntil` still counts as the run being in flight.
- **Time limit.** A run is limited to 15 minutes of wall time
  (`hostd.Config.CronTimeout`), `waitUntil` work included. At the limit the
  run is **cancelled**, not abandoned: the host drops the invocation and
  workerd cancels `scheduled()` and its `waitUntil` work, so a timed-out run
  never keeps going beside the next one. The host logs
  `cron trigger timed out` (`outcome=exceededTimeout`). Two things are
  outside the run and are not cancelled: a call the run made into a Durable
  Object finishes in that object, and a synchronous loop that never yields
  can't be interrupted (there is no CPU limit, see
  [limits](limits.md#limits-that-dont-exist)). A run counts as an
  in-flight request, so a redeploy drains it like any other. A new schedule
  takes effect at the next minute after the new deployment becomes active.
- **No retries.** Outcomes (`ok`, `exception`, …) and failures go to hostd's
  log, tagged with workload, cron and `scheduledTime`.

**Known gap (#566).** No-overlap is enforced per host. A **stateless**
workload isn't fenced, so a long run that started before its host was cut
off from mgmt (or stalled) goes on after mgmt moves the workload, and the
new host's next run of that trigger can overlap it. A stateful workload's
run stops with the fence. For a job that must never overlap, use a stateful
workload or a [lock](locks.md).

**Known gap (#600).** hostd remembers the last minute it ran per host (in
`<data-dir>/cron-last.json`, so a restart keeps it: #594). If the workload
moves to a host whose clock is behind the old one's, a minute that already
ran can run again. Keep host clocks in sync (NTP), and
make a job idempotent per `controller.scheduledTime` if a rerun would hurt.

These rules are tested at two levels. `hostd:TestCronDueOncePerMinute`,
`TestCronDueNoBackfill`, `TestCronDueSkipsOverlap`,
`TestCronDueNeedsFreshHeartbeat`, `TestCronDueFollowsDeployment` and
`TestRT3_H17_CronOverlap` (the time limit) pin each rule with a chosen
clock. The data-plane chaos loop (`e2e/dataplane:TestDataplane`, scenarios
`cron` and `alarms`, nightly, [LOOP.md](../e2e/dataplane/LOOP.md)) checks
them on three real hosts under SIGKILL, SIGSTOP across minute boundaries,
cut-offs from mgmt, takeovers, moves and schedule changes. It checks that
every (trigger, minute) runs at most once across hosts (C1), that runs of a
trigger don't overlap (C2), that no host runs a trigger past its fence or
after the workload moved off it (C3), and that a new schedule takes effect
at the next minute and the old one stops (C4). It also steps every
host's wall clock back and forward, with and without a hostd restart (C1).

## Identity and security

- A cron run has **no caller**. `getCaller()` has nothing to return,
  because nobody on the tailnet made the request.
- Its outbound `fetch()` leaves from the **workload's own node**, like the
  Worker's own fetches, so your tailnet policy for the workload's tags
  governs what a cron job can reach.
- Nobody on the tailnet can trigger `scheduled()`. hostd sends the
  invocation over the instance's private ingress socket as
  `cf.tw.kind = "scheduled"`, and only hostd can set `cf.tw`: netd strips
  client identity headers, and hostd overwrites `cf.tw` on every tailnet
  request. Paths such as `/__scheduled` and `/cdn-cgi/handler/scheduled`
  reach your `fetch()` handler like any other path. The trusted platform
  wrapper also checks that the cron is one the running deployment declares.
- The platform worker turns on workerd's `service_binding_extra_handlers`
  flag only for deployments that declare crons. Tenant code never sees the
  binding it applies to.

## Local development

`tw dev --test-scheduled` serves wrangler's test route on the local dev
port. The port is on 127.0.0.1 only:

```sh
tw dev --test-scheduled
curl 'http://127.0.0.1:8787/__scheduled?cron=*/5+*+*+*+*'                 # run it now
curl 'http://127.0.0.1:8787/__scheduled?cron=*/5+*+*+*+*&time=1700000000000'  # with a scheduledTime
```

With no `cron` parameter, the route runs the first declared trigger. Only
declared triggers run. `tw dev` also runs the triggers on their real
schedule.
