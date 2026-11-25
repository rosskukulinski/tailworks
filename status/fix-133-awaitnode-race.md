# fix-133-awaitnode-race

Last update: 2026-11-17 PT. Fixes issue #133: one node start and one
awaitNode per object start; awaitNode reads the object under `h.mu`.

## Root cause

`applyPublic` runs from both `deploy` (once the deployment is active) and
`syncAssignments` (every sync), as separate goroutines with nothing
serializing them. On the first deploy of a public workload, the deploy's
call creates the public node through `ensure` and records its options in
`publicState.opts` only after `ensure` returns. A sync's call that lands in
between sees the node but no recorded options, takes the "routes changed"
path, kills the node and starts it again. Each start launched an
`awaitNode`, so two of them ran for one object:

- data race: one wrote `o.MagicDNS` (and NodeID/Status/Err) under `h.mu`
  while the other read `o.MagicDNS` after unlocking, to build the
  `mgmt.Ready` request (the CI report, objects.go:392 vs 421);
- duplicate `Ready` calls to mgmt for one node (netd's `Create` returns the
  existing node while it is pending or ready, so both waiters see it ready);
- a superseded waiter whose node was killed could mark the live, restarted
  object failed, set `live=false` and schedule `retryNode`.

## Fix

- `hostd/public.go`: `publicState.applyMu` serializes `applyPublic` per
  workload, so a second call sees the first one's recorded options and does
  not restart the node.
- `hostd/nodestart.go` + `hostd/objects.go`: each `startNodeLocked` bumps
  `object.nodeGen` and passes it to `awaitNode`; an `awaitNode` whose
  generation was superseded returns without touching the object or calling
  mgmt. `applyPublic`'s restart path bumps it too before killing the old node.
- `awaitNode` copies `MagicDNS`, `IP` and `Err` under `h.mu` and uses the
  copies after unlocking (Ready request, audit, logs).
- `hostd/fakemgmt/readycalls.go`: `ReadyCalls(objectID)` counts ready
  PATCHes (tests).

## Tests

- New `TestPublicIngressConcurrentApply` (`hostd/public_race_test.go`): runs
  `syncAssignments` back to back through the deploy and the public node's
  join and asserts one Ready; then forces the window
  (`ApplyPublicConcurrently`, 8 concurrent `applyPublic` calls with the
  options forgotten) and asserts exactly one restart (one more Ready).
  Without the fix it fails within 3 runs with the CI race report
  (objects.go:392 vs :421) and "reported ready 3 times, want 2".
- `go test -race -count=20 -run 'TestPublicIngress' ./hostd`: ok (284 s).
- `make lint`: ok. `make changes-check`: ok (no upgrade-relevant paths).
- `make test`: first run failed in `hostd` (details lost to an output pipe); `go test -race -count=1 ./hostd/` rerun: ok (264 s). Every other package: ok.
- Placement and leases are unchanged, so `make e2e-placement` was not run.

## Gaps

- `undeploy` still sets `live=false` and kills nodes without bumping
  `nodeGen` (left as is: it is in the #94 kill-window area). A rejoin after it
  bumps the generation, so a stale waiter can no longer clobber the new start.
