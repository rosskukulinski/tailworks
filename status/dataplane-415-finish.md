Last update: 2026-11-25 00:10 PT. #415's stop condition met: every scenario 3 consecutive seeds plus 2 clean 10-minute soaks, with KV and wall-clock skew added. Findings #568 (fixed in #575), #569 and #570 (need a decision).

# dataplane-415-finish

Finishes #415, the data-plane chaos loop (`e2e/dataplane`, LOOP.md,
LEDGER.md). Builds on #564 (the test-build clock hook) and #575 (the KV fix
it found). Both are merged into this branch.

## What's built

- **`skew` fault** (`skew_test.go`): steps the lease holder's wall clock,
  hostd and its workerds, 1–6 min forward or back (within ±8 min) and
  leaves it there for the faults that follow. The hosts are a
  `-tags twtestclock` build. Before the nemesis starts, the harness checks
  that the workload's `Date.now()` follows a +10 s step, in a Worker and in
  a Durable Object. Added to `fencing` and `mixed`. Linux only: the preload
  library doesn't exist on macOS, so the fault is dropped there.
- **KV** (`kv_test.go`, docs/kv.md): the dp workload gets a KV namespace.
  One sequential client puts increasing values to 3 keys, reads them back,
  and puts keys with `expirationTtl: 60` that it reads 20–40 s later and
  again after 75 s. Checker KV enforces only the documented bounds:
  read-your-writes and durability (no stale or phantom read, final reads
  after healing), and expiry, with bounds shifted by the hosts' clock
  offsets.
- D2 notes, rather than fails, an overlap within an epoch right after a
  forward clock step (#569, until it's decided).
- `/clock` in the dp workload; `TestReplay` knows KV.
- Docs: docs/locks.md (takeover counts, skew measured, the "Clocks"
  failure mode), docs/kv.md (expiry is on the host's clock; measured by),
  docs/queues.md ("Measured by" for delivery).

## Findings (ledger F4–F6, H2, H3)

- **#568 (F4), fixed in #575:** KV TTL keys vanished at once after a
  backward clock step. The backend stamped expiry on a stateless isolate's
  clock and the store object checked it on its own, which never goes back.
- **#569 (F5), needs a decision:** a forward step of the lock host's clock
  ends a live holder's lease early, so two holders overlap in one epoch.
  Tokens still protect the store.
- **#570 (F6), needs a decision:** after a backward step of S, Durable
  Object alarms are late by about S (workerd: a DO's `Date.now()` doesn't
  go back; the alarm scheduler waits on the stepped clock). For #419.
- **H3 (ADR-028):** holds for ownership. Takeovers and moves of hosts with
  stepped clocks gave no stale answers or side effects. It doesn't hold for
  application-level time (F4–F6).
- **H2 closed:** two workerds on one DO's storage after a hostd crash can't
  happen since #432 (hostd reaps before it spawns). D7 shows 0 orphans in
  every run.
- F3 (#501) confirmed fixed on Linux: fencing 101 failed D1/D2 before the
  merge and passed after it, on the same seed.

## Tests

The stop condition (LEDGER.md "Status"):

| Scenario | Seeds | Where |
|---|---|---|
| queues | 101, 102, 103 | macOS, Linux, macOS |
| fencing | 101, 102, 103 | Linux (with skew) |
| d1 | 101, 102, 103 | Linux, macOS, macOS |
| mixed | 101, 102, 103 | Linux (with skew); 102 after a KV checker fix, rerun |
| 10-min mixed soak | 1001, 1002 | Linux (4 steps, 6 takeovers), macOS (6 takeovers) |

Commands: `TW_DP_SCENARIO=<s> TW_DP_SEED=<n> TW_DP_DURATION=4m go test
-tags e2edataplane -count=1 -v -timeout 40m -run TestDataplane
./e2e/dataplane/`. macOS on this Mac, gated on 1-min load ≤ 24. Linux in
the `twd-dev` Lima VM (arm64). Also focused Linux runs:
`TW_DP_FAULTS=skew` seed 3 (found F4–F6; after #575 only F5 notes and F6
remain), and `TW_DP_FAULTS=skew,pause-takeover,cut-takeover,move` seed 7
(pass).

- `make lint`: pass.
- `make test` (macOS): pass after the last merge of origin/master (which
  brought #598). Before it, `hostd:TestD1`, `TestPublicIngress` and
  `TestContainerPolyfill` failed once at load 30 with `upstream_unavailable`
  from the stall fence (a 1 s fence window in those tests, #591) and passed
  on rerun.
- After that merge (which brought #418's workflows harness): a 60 s
  `queues` run and a 45 s `workflows` run pass. The skew hook is off in
  the workflows test (its cluster has no clocks).
- `make lint-docs test-docs`: pass.

## Gaps

- Joining the multimgmt Postgres replicas is the #421 agent's.
- macOS runs have no skew (no preload library there).
- F6 isn't gated: no invariant checks liveness (Pulse's rate).
