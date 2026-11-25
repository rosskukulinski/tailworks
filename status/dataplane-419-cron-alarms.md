Last update: 2026-11-25 00:25 PT. Clock-step faults added on the #564 hook: two more findings, #594 (fix PR #605) and #600 (needs a decision). Both scenarios have 3+ clean seeds.

# Data-plane chaos loop: cron triggers and DO alarms (#419)

Builds on the `e2e/dataplane` harness (#415): same cluster, nemesis and
recorder pattern, two workloads of its own. Details are in
`e2e/dataplane/LOOP.md` "Cron triggers and alarms", and results in
`e2e/dataplane/ledger/cron-alarms.md`.

## What's built

- `e2e/dataplane/testdata/cron/`: `cron`, a stateless workload with three
  triggers (one holds 75 s, one holds 70 s in `ctx.waitUntil()` every 3rd
  minute). `cronobj` is stateful: the same triggers, plus alarm chains on a
  plain DO, a facet (Host-DO-delegated alarm) and an Agents SDK
  `scheduleEvery` agent. Everything reports to a recorder node.
- `cron_test.go`: `TW_DP_SCENARIO=cron|alarms` runs it from
  `TestDataplane`. Moves and takeovers move both workloads, and the
  stateless one goes without `--replace` on a takeover. Two new faults:
  `pause-boundary` (SIGSTOP across a minute boundary, under or past
  `FenceAfter`) and `reschedule` (an in-place redeploy that swaps one
  trigger).
- `check_cron_test.go`: C1 (once per minute across hosts), C2 (no overlap),
  C3 (fence, stale host, skipped minutes bounded by faults), C4 (schedule
  change), A1 (alarms: no stale or two-host fire, at least once), C5
  (convergence). `TestReplayCron` re-checks a recorded run.
  `check_cron_self_test.go` `TestCronCheckers` plants one violation of each
  and needs no cluster.
- Shared-file edits: `nemesis_test.go` (a `cron` field, a one-line hook
  in `moveTo`, two faults, two scenarios), `chaos_test.go` (dispatch to
  `runCron`), Makefile `e2e-dataplane-cron`, a nightly row, a LEDGER.md
  pointer line.
- Docs: `docs/cron-triggers.md` (the known gap #566, and which tests cover
  each rule), `docs/COMPAT.md` cron and alarm rows.

## Findings

- **#566** (CONFIRMED, needs a decision): a stateless workload's in-flight
  cron run on a cut-off or stalled host goes on after the takeover, beside
  the new host's next run (no-overlap holds per host only). Option 1:
  cancel in-flight runs at `FenceAfter`, but then a mgmt outage of more than
  30 s cancels every long job. Option 2: document it. C2 notes these
  overlaps instead of failing on them until it's decided.
- **#501** (fixed by #556): before #556, the same seeds showed 24 and 18
  stale alarm fires and a stateful cron run acting after the takeover of a
  stalled host, on macOS. Since #556: 0.
- Harness: a lost `fire` report (egress down at `crash-hostd`) looked like
  a lost alarm, so `alarm()` now throws when its report fails. There were
  also two checker fixes (the stale-host window, and sets made by a stale
  copy).

## Tests

- `TW_DP_SCENARIO=cron`, 4m: seeds 4 and 7 (macOS), 11 and 13 (Linux arm64,
  Lima twd-dev): pass. Seed 2 passes on replay.
- `TW_DP_SCENARIO=alarms`, 4m: seeds 3, 5 and 6 (macOS), 12 (Linux arm64):
  pass.
- `go test -tags e2edataplane -run TestCronCheckers ./e2e/dataplane/`: pass.
- `make lint`: pass. `make changes-check`: ok (nothing upgrade-relevant).
- `make test`: see the PR.

## Clock steps (2026-11-25)

Once #564 landed: `clock-back` and `clock-forward` faults step every host's
wall clock (a `twtestclock` build of hostd, one shared offset file, half the
time with a hostd restart mid-step).

- **#594** (fix: PR #605): `cronLast` was in memory only, so a restart
  under a stepped-back clock reran minutes (cron 31: 4 pairs). With #605:
  0 on the same seed.
- **#600** (needs a decision): `cronLast` is per host. A move followed by a
  step back reruns the minute the old host ran (cron 32: 4 pairs). The fix
  would be mgmt carrying it, or documenting it.
- C1 notes both, and doesn't fail on them. A rerun on one host with no
  restart still fails (`TestCronCheckers`).
- Seeds with the clock faults: cron 33 and 35 (macOS), 34 (Linux arm64):
  pass.

## Gaps and next steps

- Hosts whose clocks *differ*: the harness gives every process one
  environment, so all hosts step together.
- #566 and #600 need decisions. When #605 lands, drop the #594 case from
  C1's note and the docs' "Known gaps".
