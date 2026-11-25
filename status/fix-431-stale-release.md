Last update: 2026-11-24 20:10 PT. #431 fixed: a release-only settle is accepted from an older epoch of the consumer's lease.

# Queues: hand-back after a lease move (#431)

Found by the data-plane chaos loop (`e2e/dataplane`, F1).

- `mgmt/queuestalerelease.go`: `ackRole` picks a new role, `queueAsReleaser`,
  for a settle that only releases. `fenceStaleRelease` accepts an epoch up
  to the lease's current one (the current one only from its holder). The
  claim's lease token stays the capability, and `SettleQueue` counts
  superseded leases as stale.
- `mgmt/queues.go`: the role constant, the fence choice in
  `authorizeQueue`, and `AckQueue` passing `ackRole(a)`.
- docs/queues.md "Delivery": a batch that never ran is handed back
  uncounted, even when the workload moved away first.

## Tests

- `TestQueuesStaleRelease` fails without the fix (409 lease_lost) and
  passes with it.
- `go test ./mgmt/ -run TestQueues`: pass. `make test-pg`: pass.
- `e2e/dataplane` queues seed 5: refused hand-backs 8 → 0 (of 11), attempt
  gaps 38 → 22, all invariants pass.
- `make lint`, `make test`: see the PR.
