Last update: 2026-11-25 13:00 PT. Two mgmt test flakes fixed at their cause (#417, #541); test-only.

# fix-mgmt-flakes

## #417 mgmt TestWarmKeysRevokedOnCloseInParallel

The budget (1.6 s) covered all of `Close`: `FlushDeletes`,
`unregisterReplica` (up to 2 s), waiting for the loops, then the
revocations. The test now counts concurrent key DELETEs at the fake API
(`keyDeletesPeak`) and requires at least 2. Checked that it catches the
regression: with `warmRevokeParallel = 1` it fails ("at most 1 DELETE in
flight: revoked one by one?"); with 8 it sees 8.

## #541 mgmt TestWorkloadReplicas (no_host: h1, h2, h3 not live)

Hosts registered once and never heartbeat, so they were live for
`HostLeaseTTL` (10 s) only; a loaded run is 21 s. The test now uses
`keepHostsLive` (the #183 helper). Each subtest gets its own client
(`replicasClientFor`), so a failure is reported by the subtest, not as
`panic(nil) or runtime.Goexit` from a FailNow on the parent. Reproduced:
an 11 s sleep before `UndeployStopsReplicas` gives the exact CI error
without the fix and passes with it.

## Tests

`make lint` and `make test` passed locally (macOS, after merging
origin/master). `go test -race -count=5 -run TestWarmKeysRevokedOnCloseInParallel ./mgmt/`
and `-count=3 -run TestWorkloadReplicas` passed.

#429 (netd) was dropped from this branch: its cause is not settled. See
the issue.
