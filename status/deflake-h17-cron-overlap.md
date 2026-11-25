Last update: 2026-11-21 PT. Deflake `hostd.TestRT3_H17_CronOverlap`: test timing, not a cron overlap regression.

# deflake-h17-cron-overlap

## Symptom

CI run 44972964671 (commit 562d7b7) failed with
`next tick started 1 runs while the previous run was still in flight (overlap)`.
Later runs of the same branch passed.

## Root cause: the test, not the cron runner

The test used one short `CronTimeout` (2 s) for two jobs: bounding the run
it then checked for overlap, and checking that a timed-out run gets cancelled.
It started the first run, polled `GET /runs` until it saw the run's `start`
record, and then ticked the trigger again. Nothing bounded how long that
poll took. When the first `/runs` answers were slow, the poll saw `start`
only after the 2 s timeout had already cancelled the run and cleared its
running mark. The next tick then correctly started a new run, which the test
read as overlap.

Evidence (hostd log from a loaded local failure, `TW_TEST_LOG=1`):

- `cron trigger timed out ... scheduledTime=2027-01-04 ... ms=2002` (first
  run cancelled at 2 s)
- then the test's second tick fired 5.19 s after the first run started (an
  instrumentation line added for diagnosis and then removed). In passing runs
  it fired 20 to 50 ms after.

Failing runs took about 8.4 s and passing ones about 9.7 s. That matches the
second tick running its own full 2 s timeout after a late `start`. The CI
failure took 9.89 s, which fits the same shape with slower setup.

The product path is correct. `cronDue` skips a trigger while it has a
running mark. `runScheduled` clears the mark only when `invokeCron` returns,
and that happens when the platform answers, after `scheduled()` and its
`waitUntil` work settle (`tw-rt.js` `scheduledCtx`/`drain`), or at the
timeout, when the dropped connection cancels the run in workerd. No overlap
was seen in any run.

## Fix

- `hostd/testdata/apps/cron/src/index.js`: the `"1 0 4 1 *"` run's
  `waitUntil` work now holds until the test releases that run
  (`POST /release?t=<scheduledTime>`, stored in the `Log` DO) instead of
  sleeping `SLEEP_MS`.
- `hostd/cron_overlap_test.go`: three steps, each waiting on a real signal.
  1. Overlap, with a timeout no step reaches (1 min). The first run is held,
     so the second tick must start nothing, and the first tick must not
     return until the test releases the run. Then it records `end`.
  2. Cancellation, with a 2 s timeout set through `SetCronTimeout`. A run
     that is never released must last at least the timeout and must have
     started. Releasing it afterwards must not produce an `end` record.
  3. A cancelled run frees the trigger: the next due minute runs (released
     up front, so it finishes).
- `hostd/export_test.go`: `SetCronTimeout` (test only).

## Tests

- Before, `go test -race -count=30 -run 'TestRT3_H17_CronOverlap$' ./hostd`:
  30/30 pass (no natural failure on an idle machine).
- Before, same with `-count=15 -cpu 1,2` and 18 `yes` CPU burners: **3/30
  failed** with the CI message. A second loaded run with `TW_TEST_LOG=1 -v`
  failed again and gave the log above.
- Fault injection: a 2.5 s sleep before the second tick (a slow `/runs`
  poll). Old test and fixture: **3/3 fail** with the CI message. New test:
  3/3 pass.
- After, `-count=15 -cpu 1,2` with 18 CPU burners: **30/30 pass**.
- After, `go test -race -count=30 -run 'TestRT3_H17_CronOverlap$' ./hostd`:
  30/30 pass.
- `go test -race -run 'TestRT3_H17_CronOverlap$|TestCronTriggers' ./hostd`: pass
  (`TestCronTriggers` shares the fixture).
- `make lint`: pass. `make changes-check`: ok (no fragment needed).
- `make test` (on mbp via `mbp-test.sh test`): pass (exit 0, hostd ok, JS 42/42).

## Known gap

Step 2 requires the timed-out run to record `start` within the 2 s timeout
(it takes tens of ms in practice). Only the late observation of `start`, not
its recording, was ever slow here.
