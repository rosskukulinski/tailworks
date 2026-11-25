Last update: 2026-11-24. A new object's egress waits for mgmt to accept its ready report, so receivers narrow its caps by lim from the first call (#331).

# fix-331-cert-race: no un-narrowed first calls from a new object

## What

- netd: `Config.EgressGate` (`netd/egressgate.go`), asked by
  `EgressHandler` and `DialEgress` after `o.Ready` with the object's ID
  and its current stable node ID. Every tenant egress goes through one of
  the two (fetch, `connect()`, the Node tier's proxy, service bindings,
  OTLP export from the workload node).
- hostd: `holdEgress` (`hostd/egressgate.go`) returns once mgmt has
  accepted a ready report for exactly that stable node ID
  (`object.report`, in memory only, so after a restart each node reports
  again before it sends). It waits on a wake channel that
  `noteReport` closes after every report outcome (`awaitNode`,
  `assignNode`, `retryReady`).
  - Bounded: `reportWait` 20 s (the report's own timeout is 15 s) or the
    egress's ReadyTimeout context, then `identity_pending`.
  - A failed report refuses at once with `identity_pending` and the
    reason. A later egress re-sends the report (`rereport`, at most every
    5 s, one at a time, lease epoch refreshed), so a transient mgmt failure
    heals without a node restart.
  - A node ID mgmt accepted earlier stays good when a later report of the
    same node fails (mgmt still has it). A re-minted node is held until its
    new ID is reported.
  - Unknown netd ID, another tenant's object: `unknown_object`; killed:
    `killed`.
  - No deadlock: the ready report never depends on the object's egress;
    an object calling out from its constructor waits like any first call.
- hostd `certSource`: "not a tailworks object" is cached 2 s
  (`certNoCertTTL`) instead of 60 s; verified certificates stay 60 s.
- `hostd/servicebind.go` `awaitReported` (the #316 workaround) removed.
- Not done (per the issue): failing closed for tailworks-tagged nodes
  without a certificate (option 3).

## Tests

- `hostd/certrace_test.go` `TestFirstCallNarrowedBeforeReadyReport`, on
  `hostdtest.BackendReal` and fakemgmt. Ready reports are delayed 1.5 s
  (new `Hooks.SetReadyFault`), so the spawned child's first egress always
  precedes its certificate. The callee sees `tw.objectId` and a capMap
  narrowed to the lim on the first call. With failing reports the call is
  `identity_pending`, and once mgmt recovers a later call re-sends the
  report and goes through narrowed. With the gate disabled, all four
  subtests fail (the callee sees the full capMap and no `tw`).
- `hostd/egressgate_internal_test.go` `TestHoldEgress`: wait/wake,
  bound, re-mint, failed report, unknown/foreign/killed.
- Real-mgmt test policy (`hostdtest/realmgmt.go`) now grants `tag:ttest`
  the MCP cap the hostd suites' testcontrol hands out, so spawns may
  delegate it.

Commands run:

- `go test ./hostd -run 'TestFirstCallNarrowedBeforeReadyReport|TestHoldEgress' -count=1` (and `-race` for TestHoldEgress): pass; with the gate commented out: fail as described.
- `make lint`: pass.
- `make test` on mbp (`mbp-test.sh test`): netd, hostd and every other package pass except two mgmt timing tests
  (`TestWarmKeysRevokedOnCloseInParallel`, `TestWarmKeyRefillRespectsThrottle`,
  untouched code, a loaded runner); both pass locally with `-race`.
  `make test-js` locally: pass.
- Skipped: `make e2e-compat` (needs tailnet credentials; nightly runs it).
