Last update: 2026-11-17 PT. Fix #94: a kill returns only after the in-process kill landed.

# fix-94-kill-window

## Root cause

Several paths call `killLocal` for the same object on a host: mgmt's
`/v1/push/kill`, the heartbeat's `RevokeObjects`, the workload's own `kill`
op and a kill during a join. mgmt's `Kill` records the revocation
(`AddRevocation`) before it pushes, so a heartbeat that lands during the
push window also returns the object in `RevokeObjects`, and hostd starts
`go killLocal(...)` for it.

`killLocal` marked the object `killed` at its start and returned at once for
any caller that found it already `killed`. When the heartbeat's run won, the
push's `killLocal` returned immediately, the push answered 200, and
`mgmt.Kill` (and `tw kill`) returned while the heartbeat's run was still
between the netd kill (tailnet path: 410) and the workerd admin kill (the
platform DO's `__killed` marker). The workload's in-process path to the
object still reached the live DO and answered 200 from the same instance.
That matches the failure in #94 exactly (direct 410, `/probe/c/` 200,
`boots:1`) and the CLI sightings ("via workload after kill: 200").

A second, smaller gap: `killLocal` ignored the workerd admin answer. If
workerd was busy or restarting, the push still answered 200.

## Fix

- `hostd/kill.go` (new): `killRun` per object. The first `killLocal` creates
  it; every other caller waits for it (`awaitKill`) and gets its result.
- `killInProcess` checks workerd's answer, retries twice (50 ms, 250 ms) and
  returns an error if workerd never confirms. `killLocal` returns that error.
- The push handler answers 503 `hostd_unavailable` on that error, so mgmt
  doesn't count the kill as pushed. A later `killLocal` for the object
  (the next heartbeat's `RevokeObjects`) retries the in-process step.
- `hostd/objects.go`: one new field (`kill *killRun`) and small edits in
  `killLocal`.

#136 (`TestLockTemplate/KillLockObjectStopsIt`, 503 six seconds after the
kill) is not explained by this race alone: the race closes in milliseconds.
A 503 there means the lock DO had no killed marker, which fits an in-process
kill that failed silently. With this change that failure is no longer silent
(push answers 503, hostd logs "object not stopped in workerd", the heartbeat
retries), but #136 is not confirmed fixed.

## Tests

- New deterministic tests in `hostd/kill_internal_test.go`:
  `TestKillWaitsForRunningKill` (a second kill must wait while workerd holds
  the first one's admin kill; it fails on the old code with "the second kill
  returned (<nil>) before workerd confirmed the in-process kill") and
  `TestKillInProcessFailureIsReported` (push answers 5xx when workerd fails;
  the next kill retries).
- `go test -race -run 'TestKillWaitsForRunningKill|TestKillInProcessFailureIsReported|TestKillViaHeartbeat' -count=10 ./hostd/`: ok.
- `go test -race -v -run 'TestHostd$|TestLockTemplate$|TestKill' -count=5 ./hostd/`: ok
  (KillFailsFast and KillLockObjectStopsIt passed 5/5). The same suites at
  -count=3 also passed.
- `make lint`: ok. `make test`: ok. `make e2e-locks`: ok.
- Didn't reproduce the original flake on master locally (it's a timing race
  with the 3 s heartbeat). The new unit test reproduces it deterministically.
