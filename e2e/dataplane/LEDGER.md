# Data-plane loop ledger

Issue #415. Process: `LOOP.md`. Findings are filed as GitHub issues
labelled `dataplane-chaos`.

- Containers, pooled nodes and snapshot restore (#423, `make e2e-dataplane-vm`): [ledger/containers.md](ledger/containers.md)
- Cron triggers and DO alarms (#419, scenarios `cron`, `alarms`): [ledger/cron-alarms.md](ledger/cron-alarms.md)
- Deploys, rollback and secrets (#421, scenarios `deploys-hosts`, `deploys-mgmt`, `deploys-mixed`): [ledger/deploys.md](ledger/deploys.md)
- R2 buckets (#420, `TestR2`): [ledger/r2.md](ledger/r2.md)
- Routing (#422, `TestRouting`): [ledger/routing.md](ledger/routing.md)
- Workflows and approvals (#418, scenarios `workflows`, `workflows-move`): [ledger/workflows.md](ledger/workflows.md)

## 10-minute soaks of the follow-up loops

2026-11-25, macOS, seed 2001, `TW_DP_DURATION=10m` (`TW_DPK_DURATION` for
containers). Each loop's widest scenario; every invariant passed.

| Loop | Scenario | Faults | Result |
|---|---|---|---|
| Workflows (#418) | `workflows-move` | 28 | pass. W4 notes resumptions after a move back (#565) |
| Cron (#419) | `cron` | 19 | pass. C1 notes 3 reruns across a move (#600) |
| Alarms (#419) | `alarms` | 25 | pass. A1: 633 fires, 0 stale, 0 on two hosts |
| R2 (#420) | `r2-s3` | 28 | pass. R5: 1258 blobs in the backend, 10 referenced at quiescence (orphans; compare #580 for `fs`) |
| Deploys (#421) | `deploys-mixed`, 3 mgmt replicas on Postgres | 27 | pass. V1: 184 epochs, 0 stale answers |
| Routing (#422) | `routing` | 23 | pass. N1/N2 clean; bindings took 55–65 s to reach the new copy after two of the moves (N6 reports, doesn't gate) |
| Containers (#423) | `mixed`, vz | 32 | pass. K4: 0 VMMs outlived a hostd crash, 0 leaked pool nodes |

## Status

| Scenario | Clean seeds (consecutive) | Notes |
|---|---|---|
| queues | 101 (macOS), 102 (Linux), 103 (macOS) | Earlier: 1, 2, 5, 6. Attempt gaps inside fault windows: see F1 (#431) |
| fencing | 101, 102, 103 (Linux, with `skew`) | Earlier: 2, 7. 101 failed D1/D2 before the F3 fix and passed after it (same seed) |
| d1 | 101 (Linux), 102, 103 (macOS) | |
| mixed | 101, 102, 103 (Linux, with `skew`) | 102 failed KV on a checker race (an op sent while a step was applied) and passed on replay and on rerun after the fix. D2 notes 2 overlaps right after forward steps (F5, #569) |
| 10-minute mixed soak | 1001 (Linux, 4 skew steps, 6 takeovers), 1002 (macOS, 6 takeovers) | |

The stop condition of #415 is met (2026-11-24, branch
`dataplane-415-finish`): 3 consecutive seeds on every scenario and 2 clean
10-minute `mixed` soaks, with the KV checker and the `skew` fault. Two
things are allowed rather than gated: D2 overlaps right after a forward
clock step (F5, until #569 is decided), and Pulse going quiet after a
backward step (F6, #570: no invariant checks liveness). Nightly runs
`mixed` with the date as the seed, on Linux, so with `skew`.

## Findings

| ID | Verdict | Invariant | Issue | Summary |
|---|---|---|---|---|
| F1 | FIXED (#431) | D3 (attempts count deliveries) | #431 | A hand-back (`Release`) from a host that has just lost the lease is refused with `lease_lost`. The batch waits out the visibility timeout and is counted as an attempt it never ran. Poison can be dead-lettered after fewer real runs than `max_retries + 1`. Root cause: `mgmt.AckQueue` checks the current epoch before it looks at a release-only settle. Refused 1–9 times per run, every time on the host that had just lost the lease |
| F3 | FIXED (#501) | D1 (one live copy: side effects) | #501 | A host stalled (SIGSTOP of hostd alone, or of the whole host) past the lease, then resumed after a `--force-takeover`, made DO egress calls 2.7 s and 4.8 s after the new copy's first. hostd fences only after a *failed* heartbeat (`hostd/host.go` `tick`). On resume the first heartbeat succeeds and carries the revoke, and until it's processed nothing gates stateful egress or ingress on heartbeat freshness (cron does, `hostd/cron.go`). Not seen on macOS in 26 takeovers. Fix: a stateful workload's ingress and egress are gated on heartbeat freshness (`hostd/stallfence.go`), and freshness counts from when the heartbeat was sent. Regression: `hostd:TestStallFence`, `TestStallFenceAnswerFromBeforeStall` |
| F4 | FIXED (#568, PR #575) | KV (a put is read back until it expires) | #568 | After a backward clock step, keys put with `expirationTtl: 60` were missing 20 s later (12 in one 2-minute skew run, Linux). `parsePut` (`hostd/gen/js/kv.js`) stamped the expiry on the stateless `KVBackend`'s `Date.now()`; the store Durable Object checks it on its own, which never goes back. Fix: the store object stamps it. Regression: `hostd:TestKVTTLAfterClockStepBack` (workerd under the skew library; fails before the fix) |
| F5 | CONFIRMED, needs a decision | D2 (no two holders within an epoch) | #569 | A forward step of the lock host's clock ends a live holder's lease early: 2 overlaps in one 2-minute skew run, each right after a forward step. The lock template measures leases with `Date.now()` in the Lock object. The fenced store accepted nothing stale (tokens hold). D2 records these as notes (`forwardStepBefore`) until #569 is decided |
| F6 | CONFIRMED, needs a decision | liveness (not gated) | #570 | After a backward clock step of S, Durable Object alarms are delayed by about S: Pulse (`setAlarm(Date.now() + 250)`) went silent for the rest of a run after a 3m56s step back, through later forward steps. A DO's `Date.now()` doesn't go back, and the alarm scheduler waits on the stepped clock. workerd's behaviour. Relevant to #419 |
| F2 | FIXED (#432) | D1 (one live copy), resources | #432 | workerd runs in its own process group, so it outlives a hostd crash (SIGKILL / OOM / panic), and no later hostd reaps it: 8 of 8 `crash-hostd` faults left the old workerd running. systemd's `KillMode=mixed` covers Linux service hosts. launchd, `tw up --separate-host`'s supervisor and `tw dev` don't. Two workerds sharing one DO's storage is a hypothesis, not yet shown. Also: `e2e/queues` step 3 believes it kills workerd and doesn't |

## Hypotheses

| ID | Status | Hypothesis | Result |
|---|---|---|---|
| H1 | CONFIRMED on Linux → F3 | A host SIGSTOPped past its lease, resumed after a takeover, answers or acts (Pulse alarm egress) before its first heartbeat tells it the lease is gone. hostd fences only after a *failed* heartbeat (`hostd/host.go` `tick`) | No stale answer or side effect in 26 takeovers of a stopped or cut-off host (fencing 2, 7; d1 4; mixed 3, 8, 9). On SIGCONT the first heartbeat succeeds and carries the revoke first. The window is real in principle (a request that reaches the ingress before that heartbeat returns). Keep it in the mix. **On Linux it shows:** the first CI run (ubuntu-24.04, mixed 20261125) had 2 stale side effects, 2.7 s and 4.8 s after the new copy's first: F3, #501 |
| H2 | CLOSED (moot since #432) | Two workerds on one DO's storage after a hostd crash (F2) corrupt or lose DO writes | It can't happen any more: hostd records every workerd it starts in `<data-dir>/children.json` and kills those still running in `New`, before it spawns anything (`hostd/childreap.go`). D7 gates it: 0 orphans in every `crash-hostd` since (queues 6 on the fix branch, queues 101–103, mixed 101–103), with D1-reg clean across those restarts. What's left is a crash between `cmd.Start` and the ledger write (microseconds), not worth a test |
| H3 | PARTLY REFUTED → F4, F5, F6 | Wall-clock skew on a host doesn't matter (ADR-028) | True for ownership: with the host's clock stepped ±1–6 min (hostd and workerd, the `skew` fault), takeovers and moves of skewed hosts gave no stale answers or side effects (D1), no cross-epoch overlap (D2) and no lost queue message (D3). Not true for application-level time on the host: KV TTLs (F4, fixed), the lock template's lease (F5) and DO alarms (F6). The root of F4 and F6: a Durable Object's `Date.now()` never goes back (measured: +10 s then back to 0, the Worker reads 0 and a DO stays ahead until the clock catches up) |

## Harness notes (not product bugs)

- `--replace --force-takeover` moves the lease at once, even off a host that
  is cut off or stopped. The first fencing run used it for takeovers and D1
  failed (78 stale answers, 132 stale side effects, lock holders
  overlapping). That's the documented failure mode in docs/locks.md
  ("`tw deploy --replace --force-takeover` on a live host"). Takeovers now
  use `--force-takeover` alone, which waits for the lease, as
  `TestLocksFailover` does. The run shows the checkers catch a real split
  brain.
- `faultAt` picked the first fault whose widened window held the time, not
  the latest. A `move` right after a `pause-takeover` was blamed on the
  takeover (d1 seed 4, 3 false D2 overlaps). Fixed. `TestReplay` on that
  run's history is clean.
- A stateful workload can't move without `--force-takeover`
  (`move_unconfirmed`). The `move` fault uses `--replace
  --force-takeover`.
- hostd's workerds are not in its process group. Host-wide faults signal
  hostd's descendants (`descendants()`), and the `crash-hostd` fault
  deliberately doesn't.

## Run log

| Run (results dir) | Scenario / seed | Verdict |
|---|---|---|
| 20261125T015744Z-queues-1 | queues 1, 60 s smoke | pass. Harness fixes after it: workerd lookup, stateful moves, ack-before-throw consumer |
| 20261125T020048Z-fencing-2 | fencing 2 | HARNESS-BROKEN: replace+force takeovers (see notes) |
| 20261125T020048Z-queues-2 | queues 2 | pass. F1 seen (7 refused hand-backs) |
| 20261125T020609Z-fencing-2 | fencing 2 | pass. 6 safe takeovers at 43–48 s |
| 20261125T021247Z-mixed-3 | mixed 3 | pass |
| 20261125T021247Z-d1-4 | d1 4 | D2 false positive (faultAt). Clean on replay after the fix |
| 20261125T021247Z-queues-5 | queues 5 | pass. F1 (8 refused hand-backs) |
| 20261125T022506Z-queues-6 | queues 6, crash-hostd + move | pass on the invariants. F2: 8 of 8 crashes left workerd running |
| 20261125T022948Z-fencing-7 | fencing 7, pause-hostd-takeover + pause-takeover | pass. 6 takeovers of a host whose hostd was stopped and workerd kept running |
| 20261125T022948Z-mixed-8 | mixed 8 | pass. F2 seen again (2 of 2) |
| 20261125T022948Z-mixed-9 | mixed 9 | pass. F2 seen again (1 of 1) |
| fix-431 branch, queues 5 | queues 5 | pass. Refused hand-backs 8 → 0 (of 11), attempt gaps 38 → 22 (the rest are crashes and pauses) |
| make e2e-dataplane (macOS, merged tree) | mixed 20261125 | pass |
| CI ubuntu-24.04, nightly dispatch run 45535497016 | mixed 20261125 | **D1 fail: F3** (2 stale Pulse side effects after takeovers of stalled hosts) |
| fix-432 branch, queues 6 | queues 6, crash-hostd + move | pass. Workerds alive 10 s after a hostd crash and restart: 8 of 8 → 0 of 7 (D7 now gates it) |
| fix-501 branch, Linux arm64 (Lima) | fencing 20261125, pause-takeover + pause-hostd-takeover, 5 min | pass. 6 takeovers of a stalled host, 7 epochs, 0 stale answers, 0 stale side effects (of 371 pulses) |
| dataplane-415-finish, Linux (before merging the F3 fix) | fencing 101 | D1 4 stale answers, 4 stale side effects; D2 1 overlap: F3 again (pause-takeover of h3). Same seed after the merge: pass |
| dataplane-415-finish, Linux | fencing 7, `TW_DP_FAULTS=skew,pause-takeover,cut-takeover,move` | KV fail on the first checker (expiry seen by a clock stepped back after the key had expired); pass on replay after the checker fix. 4 takeovers, 2 with h3 skewed −5m1s |
| dataplane-415-finish, Linux | fencing 3, `TW_DP_FAULTS=skew`, 2 min (19 steps) | **F4** (12 TTL keys missing, #568), **F5** (2 overlaps after forward steps, #569), **F6** (Pulse silent after the first step back, #570). D1, D3, D4 clean |
| dataplane-415-finish + #575, Linux | fencing 3, `skew` only, 2 min | KV clean (F4 fixed); F5 2 notes; F6 again (62 pulses) |
| dataplane-415-finish + #575, Linux | fencing 7, skew + takeovers | pass. A −5m1s step: the Worker read −300.8 s, a DO −0.5 s (doesn't go back) |
| dataplane-415-finish + #575 | queues 101–103, fencing 101–103, d1 101–103, mixed 101–103, soaks mixed 1001 (Linux, 10 min) and 1002 (macOS, 10 min) | pass (mixed 102 after a KV checker fix: see Status). Stop condition met |
