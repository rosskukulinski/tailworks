Last update: 2026-11-22 16:00 PT. Redeploy after a long restart no longer fails with lease_lost; egress Status race fixed; mgmt sets OmitAuth.

# fix-redeploy-epoch-telemetry-race

Two problems found while working on #245 (PR #253). Neither had an issue.

## 1. Redeploy with an expired lease (product bug)

With `OmitAuth` set on mgmt's LocalAPI client (ADR-019 rule 4),
`make e2e-tw-upgrade` failed: after the good upgrade the embedded host's
redeploy got `lease_lost` on its secret grant
(`not held by "tw-tu" at epoch 2097153`) and the deployment stayed failed.

The epoch was right. The lease had **expired**:

- mgmt's grants (secrets, volume keys, stores, telemetry) refuse a lease
  whose `expires_at` has passed (TTL 45 s). Only a heartbeat renews it, and
  `RenewLease` accepts an expired lease nobody else took.
- hostd's first heartbeat after a start goes out before it knows any
  workload, so it renews nothing. The deploy that follows asks for its
  secrets before the next heartbeat.
- In e2e-tw-upgrade the store is restored from a backup on each rollback,
  so the lease's `expires_at` is from the first run; the restarts that
  followed added up to more than 45 s. `OmitAuth` only moved the timing
  past that point. The same happens to any host down for over 45 s.
- A failed deployment is not retried (`wantDeploy`), so it stayed failed.

Fix (`hostd/deploylease.go`): `deploySecrets` asks at the workload's
current epoch. On `lease_lost` it refreshes the epoch from the assignments
(as `readyretry.go` does), sends one heartbeat right away (it carries the
"starting" deployment's lease), and asks again if mgmt renewed it. That
heartbeat's revocations are left to the next regular one, because
`undeploy` would deadlock on the deploy's `deployMu`. `heartbeat` is split
into `heartbeatRequest` plus send. mgmt's semantics are unchanged.

Then `internal/mgmtd` sets `OmitAuth` on the mgmt node's shared LocalAPI
client before anything uses it (ADR-019 rule 4: every tsnet LocalClient).

Test: `hostd` `TestRedeployAfterLeaseExpired` (real mgmt, store clock moved
past the TTL across `Env.Restart`). It fails before the fix with the e2e's
`lease_lost` and passes after. `hostdtest.Options.MgmtClock` sets the real
mgmt's clock and its store's.

## 2. Data race in hostd TestTelemetry

`http.go:159` (`egressObjHandler`) read `o.Status` without `h.mu`, while
`objects.go:402` (the node join) wrote it under `h.mu`. That is product
code, not the test. The read is now under `h.mu`.

Test: `hostd` `TestEgressObjHandlerReadsStatusUnderLock` reports the race
every time under `-race` before the fix and is clean after.
`TestTelemetry -race -count=50` without the fix did not reproduce it
(0 races in 50 runs), which is why the deterministic test exists.

## Also changed

`e2e/twupgrade` `checkData` waits (up to 30 s) while the app is still
"starting". One run after the fix failed because the check came 30 ms
before the rolled-back release's redeploy finished: a rollback returns
when `tw up` is back, not when the app is active again.

## Tests run (after merging origin/master with #253)

- `make lint`: PASS.
- `make test` (mbp runner): PASS.
- `make e2e-tw-upgrade` with `OmitAuth` set: before the fix 1 of 1 FAIL
  (`lease_lost`, as in #253). After the fix, before the checkData wait:
  2 PASS, 1 FAIL (the "starting" check above; its log shows the renewal
  working). After the wait: 4 of 4 PASS.
- `make e2e-upgrade`: PASS.
- `go test -race -count=50 -run 'TestTelemetry$|TestEgressObjHandlerReadsStatusUnderLock' ./hostd/`: PASS, 0 races.
- `TestRedeployAfterLeaseExpired`: fails without the fix, passes with it
  (`-race -count=3`), alongside `TestRealMgmtStores`.
