Last update: 2026-11-21 PT. Fix #181: data race between instance.onExit and instance.waitHealthy.

# deflake-181

## Root cause

A real (if small) data race, not a test problem. `instance.onExit`
(`hostd/restart.go`) bumps `inst.restarts` under `inst.mu` each time a
workerd exits. `instance.waitHealthy` (`hostd/workload.go`) took `inst.mu`
to read `inst.exited`, released it, and then read `inst.restarts` with no
lock. When a workerd exits during startup (`TestWorkerdDeployTimeFailure`
runs a workerd that exits at once), the exit goroutine's `onExit` can write
the count between `waitHealthy`'s unlock and its read. That is the pair
`restart.go:133` / `workload.go:959` in the CI report.

The window is a few instructions wide, so the integration test hit it only
rarely: `go test -race -count=15 -cpu 1,2,8 -run 'TestWorkerdDeployTimeFailure$' ./hostd`
(45 runs) and `-count=20` (20 runs) on master found no race on this machine.

## Fix

`waitHealthy` reads `restarts` together with `exited`, under `inst.mu`.

## Tests

- New `TestWaitHealthyDuringRestart` (`hostd/restart_internal_test.go`):
  `waitHealthy` against an exited workerd while another goroutine bumps
  `restarts` under `inst.mu` in a tight loop, as `onExit` does.
  - On master's `workload.go`: `go test -race -count=3 -run TestWaitHealthyDuringRestart ./hostd`
    FAIL (DATA RACE, `workload.go:960` read vs the test's locked write);
    `-count=20`: FAIL (race reported in the first process). The race
    detector reports a given pair once per process, so one failing run per
    process is the most it shows.
  - With the fix: `go test -race -count=50 -cpu 1,2,8 -run TestWaitHealthyDuringRestart ./hostd`:
    ok, 150 runs, no race.
- `go test -race -count=15 -cpu 1,2,8 -run 'TestWorkerdDeployTimeFailure$' ./hostd`: ok (45 runs).
- `make lint test` (macOS, after fetching origin/master: no new commits): ok.
