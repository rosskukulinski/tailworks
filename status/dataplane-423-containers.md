# dataplane-423-containers

Issue #423: the data-plane chaos loop for containers, pooled nodes and
snapshot restore. Builds on the `e2e/dataplane` harness (#415).

## What it adds

- `e2e/dataplane/containers_test.go`, `check_containers_test.go` (build tags
  `e2edataplane e2edataplanevm`), workloads in
  `e2e/dataplane/testdata/containers/` (`ctr`: one container + one node per
  Box object; `vm`: a microVM-tier workload for restore bursts).
- Faults `api-stall`, `burst-crash`, `crash-hostd-vmm`, `kill-vmm`,
  `redeploy`, `vm-burst`, plus the shared ones. Invariants K1–K4
  (LOOP.md "Containers, pooled nodes and snapshot restore").
- `make e2e-dataplane-vm` (macOS vz, or Linux KVM with the test binary as
  root; not in the nightly). `make lint` vets the new tags for darwin and
  linux.
- Harness: uses `cluster.workload` and `cluster.hostArgs` (from master),
  so the shared faults (`kill-host`, `move`, takeovers) restart hosts with
  the microVM flags and move this loop's workload. No change to the shared
  harness files.
- Ledger: `e2e/dataplane/ledger/containers.md` (status, findings, run log);
  one pointer line in `LEDGER.md`.

## Results

Ran on this Mac (vz, 3 hosts) and an amd64 Ubuntu 24.04 KVM guest
(Firecracker + jailer as root, 2 hosts). 3 consecutive clean seeds per
scenario: containers (vz) 6, 7, 8; pool (vz) 5, 6, 7 and (Firecracker)
1, 2, 3; restore (Firecracker) 10, 11, 12; mixed (vz) 4, 5, 6. No
10-minute soak yet.

Findings (`dataplane-chaos`):

- #567: a host that loses the lease leaves its pool nodes ready in mgmt
  and their devices undeleted. Fixed in #583 (merged).
- #587: a crashed hostd's Firecracker VMs (in-process helper) are never
  reaped. Fix in #590 (regression test `TestSweepDeadInProcess`, root).
- #599: refuted (checker artifact); #602 closed.
- #610: a pool node minted just before a hostd crash stays minted forever.
  Needs a decision (the issue lists options).
- #611: container exec can return exit 0 with its stdout lost (Firecracker,
  concurrent execs at container start). Needs investigation.

## Tests

- `make lint`: pass. `make test`: pass (before the last changes, which
  touch only files behind the new build tags and docs; `make lint` vets
  those and passed after them).
- `make e2e-dataplane-vm` equivalent runs: see the ledger's run log.
- Not run: `make e2e-vz` / `e2e-vm` / `e2e-container` (no product code in
  this branch).
