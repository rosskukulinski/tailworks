Last update: 2026-11-21 PT. TestRT3_H10_PublicNameSquat deflaked (#183). It was a test race, not a routing gap.

## Root cause

The CI failure (run 44244985301, 2026-11-18) was
`POST /v1/deployments: 503 no_host ... no eligible host (h1: not live)` at the
"tenant on another tailnet" deploy, and the test took 12.75 s.

`publicHost` registers h1 once and never sends a heartbeat. Placement counts a
host as live only while `now - LastSeen <= HostLeaseTTL` (10 s,
`Host.Live`). So every test that uses `publicHost` had to finish within 10 s of
registering h1. Locally the test takes 2 to 6 s, but on a loaded CI runner,
where the whole mgmt package took 551 s, it went past 10 s. Public-name
routing is not involved, and a real host heartbeats every few seconds.

To reproduce it: a temporary probe set h1's `last_seen` back 11 s before that
deploy, and the run failed with the identical 503
`no eligible host (h1: not live)`.

## Fix

Test only (`mgmt/public_test.go`). `publicHost` now heartbeats h1 every
`HostLeaseTTL/10` until cleanup, the way hostd does. The heartbeat carries no
leases. It only touches `last_seen`, because absent leases are not revoked.

## Tests

- With the fix, a temporary `time.Sleep(11 * time.Second)` before that deploy:
  passes (13.6 s).
- `go test -race -count=30 -run 'TestRT3_H10_|TestPublic' ./mgmt`: ok (7 tests
  x 30, 407 s).
- 6 parallel copies of `-race -count=5` before the fix (no failures reproduced
  at 2-6 s per run; the mechanism is shown by the probe).
- `make lint test`: see the PR.
