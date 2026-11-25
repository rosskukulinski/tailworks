Last update: 2026-11-25 00:10 PT. hostd keeps each workload's last cron minute across restarts (#594).

# hostd: cron minutes that ran survive a restart (#594)

Found by the data-plane chaos loop's cron scenario (#419, PR #585) with the
clock-step faults: a hostd restart while the clock was stepped back reran
minutes that had already run (C1). `w.cronLast` was in memory only.

- `hostd/cronlast.go`: `<DataDir>/cron-last.json`, "<tenant>/<workload>"
  → last minute (Unix ms). Saved when `cronDue` marks a minute (at most
  once a minute per workload), loaded when a workload's `cronLast` is
  still zero. A stored minute more than 15 min ahead of the clock is
  ignored and logged (the clock was fast, then corrected).
- `hostd/cron.go` `cronDue`: the load and save calls. `hostd/host.go`: the
  store, opened next to the child ledger.
- `docs/cron-triggers.md` "At most once per minute": says it holds across
  restarts too.

## Tests

- `go test ./hostd -run 'TestCronLast|TestCron|TestRT3_H17'`: pass.
  `TestCronLastSurvivesRestart` fails without the fix ("after a restart, at
  00:03:10: reran").
- See the PR for `make lint test` and the e2e/dataplane cron run.

## Not in scope

A move to a host whose clock is behind can still rerun a minute the old
host ran. `cronLast` is per host, so that would need mgmt to hold it.
