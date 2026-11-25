Last update: 2026-11-23 20:30 PT. TestDevQueues/TestDevSIGKILL port race fixed; KillLockObjectStopsIt and #138 not reproduced.

## Triage

I read every failed `ci.yml` run from 2026-11-18 to 2026-11-23 and kept the
failures on PRs that couldn't have caused them (Dependabot bumps, unrelated
code) or on master pushes.

| Test | Last CI sighting | State |
| --- | --- | --- |
| `cmd/tw` `TestDevQueues` (#286) | run 45164473088 (2026-11-23, Dependabot) | **fixed here** |
| `hostd` `TestLockTemplate/KillLockObjectStopsIt` (#287) | run 45244249704 (2026-11-23, Dependabot) | not reproduced, see below |
| `hostd` `TestSpawnLimEnforcedAtReceiver` (#138) | run 44992982273 (2026-11-22, fix-158) | not reproduced |
| `mgmt` `TestStoresLimits` | run 45076332888 (2026-11-22) | fixed by #261 (merged later) |
| `netd` `TestReadyNodeRejectedLater` | run 45064906900 (2026-11-22) | fixed by #252 |
| `hostd` `TestRT3_H17_CronOverlap` | run 44972964671 (2026-11-22) | fixed by #219 |
| `cmd/tw` `TestCheckDevPort` | run 44993457008 (2026-11-22) | fixed by #243 |

Not flakes: `TestWorkerdPinMatchesLockfile` and `TestDoctor` on the workerd
Dependabot PRs (the pin really is stale), and `TestADRIndex` on a branch
with an unnumbered ADR.

## TestDevQueues (fixed)

`bind: address already in use`: the test took a port from `127.0.0.1:0`,
closed it, and passed it to `tw dev --port`. A parallel test in the package
can bind it first. Now `--port 0`, and the URL comes from the `--json`
`ready` event. `TestDevSIGKILL` did the same (it never uses the port) and
now passes `--port 0` too.

## KillLockObjectStopsIt (open)

This is a new symptom, not #136's: the 410 renew passed, and then c2's acquire got
`503 upstream_unavailable: upstream: write unix …/ingress.sock: write: broken pipe`.
That is netd's `UnixUpstream` (`netd/ingress.go`) writing to a workerd
connection that workerd had already closed.

What I checked:

- Baseline: `go test -race -count=20 -run 'TestLockTemplate$/KillLockObjectStopsIt' ./hostd`
  on master passed 20/20.
- workerd closes an idle keep-alive connection 15 s after the last response
  (measured). `UnixUpstream` keeps idle connections for 60 s. 360 concurrent
  tiny POSTs fired around that 15 s mark through `UnixUpstream`: 0 failures, so
  the idle close doesn't explain it.
- A Durable Object answering a small (200 B) POST without reading the body:
  0/600 failures. Not it either.

Found on the way (separate bug, not this flake): when a Durable Object
answers **without reading a ~16 KB+ request body**, workerd closes the
connection while netd is still writing the body, and netd returns
`503 upstream_unavailable … broken pipe` instead of workerd's response.
That reproduced in 15–44 of 200 requests for 20 KB and 200 KB bodies (all on
fresh connections). Buffering small bodies to make them replayable
(`GetBody`) didn't change it. I left the code unchanged and wrote it up as a
separate issue: #288.

Next step if it recurs: run the job with `TW_TEST_LOG=1`.

## Tests

- `go test -race -count=3 -run 'TestDevQueues$|TestDevSIGKILL$' -v ./cmd/tw`: 3/3 each.
- `make lint test` (macOS, after merging origin/master at cfb66834): ok.
