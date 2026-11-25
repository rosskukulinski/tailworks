Last update: 2026-11-24 23:20 PT. R2 data-plane chaos loop (#420): built, 3+ clean seeds per scenario, two product bugs fixed in their own PRs, one filed.

## What's built

- `e2e/dataplane` `TestR2` (`r2_test.go`, `r2client_test.go`, `check_r2_test.go`,
  `check_r2_unit_test.go`): the TestDataplane cluster running
  `testdata/r2` under three scenarios, `r2-fs`, `r2-s3` and `r2-replicas`
  (registered in `nemesis_test.go` with three new faults: `cut-s3`,
  `slow-s3`, `kill-mid-put`), checked for R1–R6 and convergence. Details
  and results: `e2e/dataplane/ledger/r2.md`; `LOOP.md` and `LEDGER.md`
  point to it.
- The `s3` backend is a Garage container (`dxflrs/garage:v2.1.0`; MinIO no
  longer publishes images), one TCP fault proxy per host.
- `e2e/chaoskit`: the TCP fault proxy that `e2e/multimgmt` used for
  Postgres (moved, `pgproxy_test.go` now wraps it) and a Docker helper that
  addresses containers by ID only.
- Shared-file edits, kept small: `harness_test.go` (cluster `workload` and
  `hostArgs` fields, `startHost` appends `hostArgs`), `chaos_test.go`
  (TestDataplane skips `r2-*`), `nemesis_test.go` (3 faults, 3 scenarios).
- `make e2e-dataplane-r2` (all three scenarios, 3 min each) and a nightly
  matrix entry; nightly uploads `e2e/dataplane/results/` when a dataplane
  suite fails.
- docs/r2.md: "How it's tested under faults" (R1–R6), and the stale-copy
  `r2_blob_missing` case with replicas on s3.
- Porcupine isn't a dependency. R1 is a sound per-key register checker
  (values are unique), so no new dependency.

## Findings

| Issue | Status |
|---|---|
| #562 store writes that change nothing are acknowledged from a stale copy (lost R2/KV delete, D1 no-op update; writes acknowledged with mgmt unreachable) | fixed by #573 (merged), regression test `hostd:TestStoreNoopWriteChecksJournal` |
| #574 an R2 put whose journal answer is lost deletes its blob though the index has the row | fixed by #576 (merged), regression test `hostd:TestR2PutLostAnswer` |
| #580 fs backend: blobs left on a host after a move are never deleted | filed, needs a decision |

## Results

Clean seeds (each 3 min): r2-fs 1, 2, 3, 4; r2-s3 1, 2, 3; r2-replicas 1, 2, 3.
The r2-fs and r2-replicas seeds count with the #562 fix merged in (both
failed seed 1 without it). Run log: `e2e/dataplane/ledger/r2.md`.

## Tests run

- `make lint`: pass. `make changes-check`: ok.
- `make test`: all packages pass except `./hostd`, which hit an intermittent data race (#586, not this branch's code).
- `TestR2Checkers`: pass. `TestDataplane` mixed seed 3, 60 s: pass.
- `make e2e-dataplane-r2` (seed 20261125, with the fixes): pass, 774 s.

## Gaps / next

- H1/F-R2-2's window (a lost answer after a commit) is only hit by chance;
  a mgmt-side delay fault (the mgmt proxy can only cut) would widen it.
- Moves of replicas aren't faulted (a replica can't be placed with
  `--host`); `r2-replicas` covers host crashes, cut-offs and S3 faults.
