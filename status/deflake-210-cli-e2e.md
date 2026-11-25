Last update: 2026-11-21 PT. #210 reproduced and fixed: hostd retries a ready report refused with lease_lost. #138 not seen again.

# deflake-210-cli-e2e

## #210: TestCLIEndToEnd identity_pending in redeploy-after-purge

Reproduced on the mbp runner. `go test -race -count=5 -run
'^TestCLIEndToEnd$' ./cmd/tw/` alone passed 5/5. Under load (the other
Go packages under `-race` running at the same time, as in `make test`),
`-count=40 -failfast` failed on its first run with the issue's error:
`agent not ready after 3m0s (status active, node minted)`.

What the logs show (supervisor, mgmt and hostd logs copied from the
failing state dir):

1. `tw down` (ephemeral, so it purges) kills and forgets the objects. The
   deployments stay assigned to the host.
2. `tw up` starts a fresh hostd. Its startup sync brings the previous
   agent deployment (epoch 1) back up and creates its node again.
3. The test's `tw deploy` lands at the same moment and bumps the lease to
   epoch 2.
4. The node joins fine, but its ready report carries epoch 1. mgmt refuses
   it: `mgmt ready failed ... lease_lost ... held by "tw-t" at epoch 2;
   caller "tw-t" asked for epoch 1`.
5. hostd treats the node as ready and never reports it again. The epoch-2
   deployment adopts that node, so mgmt's row stays `minted` and `tw
   deploy` times out.

This is the race `retryNode` (commit 94e878f) already handles when the mint is
refused with lease_lost. Here the mint succeeded and the ready report was
refused. It is a product bug: any `tw deploy` during a fresh host's
startup sync of the same workload can hit it. It is not #200. Nothing
rejoined from a netmap cache (purge removes the node state; every join in
the log is `rejoined=false`), and the hermetic control server never
rejects a node.

Fix: `hostd/readyretry.go`. When mgmt refuses a ready report with
lease_lost, hostd refreshes the workload's epoch from its assignments and
sends the report again (1, 2, 4, 8, 16 s; 5 tries). It stops when the
report is accepted, when the object was killed or restarted, when the
workload is no longer active here, or on any other error. A kill answer
kills the object, as on the first report. The epoch refresh is shared
with `retryNode`.

Test: `TestReadyRetriesAfterLeaseLost` (hostd). fakemgmt gains
`SetLeaseLostReady(n)`. The test fails without the fix (mgmt's row stays
`pending`) and passes with it.

The issue's first failure (`tw deploy` exit 1 while landing #188) left no
logs. The failure I reproduced was a `tw deploy` exit 1 with this cause, so
it is most likely the same thing.

## #138: TestSpawnLimEnforcedAtReceiver

I didn't run it again. CI history: no failed CI run since 2026-11-18 has
TestSpawnLimEnforcedAtReceiver in its log. The failed runs on 2026-11-21
were TestWorkerdPinMatchesLockfile, TestDoctor, TestLockTemplate and
TestWarmKeyExpiry. The test is not in any `GO_TEST_SKIP` quarantine. The
issue stays open.

## Tests

- mbp, before the fix: `go test -race -count=5 -run '^TestCLIEndToEnd$' -v
  ./cmd/tw/` with other packages under `-race` alongside: 5/5 pass. Then
  `-count=40 -failfast`, with more packages alongside: failed on the first
  run (identity_pending, as in the issue).
- mbp, with the fix, the same load: `-count=30`: 30/30 pass (809 s).
- `go test -race -count=1 -run
  'TestReadyRetriesAfterLeaseLost|TestNodeRetriesAfterLeaseLost' ./hostd/`:
  ok. With the retry call removed, TestReadyRetriesAfterLeaseLost fails.
- `make lint`: ok. `make changes-check`: ok.
- `make test` (mbp runner, after merging origin/master): ok.
