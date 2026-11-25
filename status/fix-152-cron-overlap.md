Last update: 2026-11-21 PT. Fix #152 (ledger H17 / F12): a cron run no longer overlaps with the next one.

# fix-152-cron-overlap

## Root cause

hostd clears a trigger's running mark when the platform answers the
`scheduled()` invocation. The runtime wrapper answered as soon as the
tenant's `scheduled()` returned, while promises it had passed to
`ctx.waitUntil()` went to the request's own `waitUntil` and kept running.
So the run looked finished to hostd, the next due minute started a second
run beside the first, and the leftover work was not bounded by the 15-minute
cron timeout either. (A plain `await` past the timeout was already cancelled:
hostd drops the connection and workerd cancels the request, which
`gen.TestGeneratedCronCancelledOnDisconnect` pins.)

## Choice

Of the two options in the issue, the run is **cancelled at the timeout**:

- `js/packages/tailnet/src/runtime.ts` (`scheduledCtx`): a cron run gets a
  ctx whose `waitUntil()` collects promises; the wrapper answers only after
  `scheduled()` and all collected promises settle (also when `scheduled()`
  throws). This matches Cloudflare, where a cron event lasts until its
  `waitUntil` work finishes. Both export forms (object, `WorkerEntrypoint`
  class) are covered.
- Because the work now belongs to the request, hostd's timeout (which drops
  the connection) makes workerd cancel it with the run. The running mark is
  held until then, so the next tick is skipped while the run is in flight.
- `hostd/cron.go`: a timeout is logged as `cron trigger timed out`
  (`outcome=exceededTimeout`); limits.md anchor `#cron-no-overlap` on the
  overlap guard.

Not cancelled, and documented: work the run started inside a Durable Object
(it finishes in that object), and a synchronous loop that never yields (no
CPU limit).

## Docs

`docs/cron-triggers.md` (Delivery: no overlap, time limit), `docs/limits.md`
(cron rows), `security/LEDGER.md` (H17 fixed, F12 H17 done, run log row),
`changes/fix-152-cron-overlap.md`.

## Tests

- `hostd.TestRT3_H17_CronOverlap` (new, `hostd/cron_overlap_test.go`, 2 s
  CronTimeout, fixture cron `1 0 4 1 *` sleeping 5 s in `waitUntil`):
  failed before the fix ("next tick started 1 runs while the previous run
  was still in flight"), passes after.
- `js/packages/tailnet/test/scheduled.test.js` (new): waitUntil work delays
  the answer and never reaches the real `waitUntil`, for object and class
  exports and a throwing `scheduled()`.
- Commands run: see the PR's Tests section.
