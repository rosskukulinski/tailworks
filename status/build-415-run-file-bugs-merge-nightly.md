Last update: 2026-11-24 18:45 PT. The data-plane chaos loop (#415) is built, has run 13 times, found 3 bugs (#431, #432, #501), and runs nightly (allow_failure: #501).

# Data-plane chaos loop (issue #415)

## What's built

- `e2e/dataplane/` (build tag `e2edataplane`): real tw-mgmt plus 3 real
  tw-hostd processes on testcontrol + a local DERP. Each host reaches mgmt
  through its own cuttable proxy. The workload `testdata/dp` holds Queues
  (with a DLQ), the lock template, a DO counter (`Reg`), a DO with alarm
  egress (`Pulse`) and D1. A recorder node and a fenced store sit on the
  tailnet.
- A seeded nemesis (`TW_DP_SEED`) with 10 faults: kill-host, crash-hostd,
  kill-workerd, pause-short, pause-takeover, pause-hostd-takeover,
  cut-short, cut-takeover, move, restart-mgmt. Scenarios: queues, fencing,
  d1, mixed.
- Checkers D1–D6 over `history.jsonl` (`check_test.go`), and `TestReplay`
  to re-check a recorded run.
- `LOOP.md` and `LEDGER.md`. `make e2e-dataplane` (mixed, 4 min, the date as
  the seed) runs in `nightly.yml`.

## Findings

- #431 Queues: a hand-back from a host that just lost the lease is refused
  (`lease_lost`), so it costs an attempt that never ran.
- #432 hostd: workerds survive a hostd crash and are never reaped.
- #501 hostd: a host resuming from a stall past its lease makes DO egress
  calls after the takeover, before its heartbeat revokes it (D1, seen on
  Linux CI). Nightly's e2e-dataplane entry is `allow_failure` on it.

## Runs

- `make lint`, `make test`, `make changes-check`: pass (macOS, after merging origin/master).
- `make e2e-dataplane` on macOS: pass (mixed 20261125). On ubuntu-24.04
  (`gh workflow run nightly.yml` on the branch): D1 fails, which is #501.

See `e2e/dataplane/LEDGER.md` "Run log": 13 runs, 3–6 min each, on an
M-series Mac. Every run since the harness fixes passes the gated
invariants.

## Gaps

- Wall-clock skew on a host: needs a hook in hostd and workerd (H3).
- Multiple mgmt replicas on Postgres: not joined with `e2e/multimgmt` yet.
- KV bounds (item 4 of the issue): not started.
- Stop condition (3 consecutive seeds on every scenario, 2 ten-minute soaks):
  not met yet. d1 has 1 seed.
