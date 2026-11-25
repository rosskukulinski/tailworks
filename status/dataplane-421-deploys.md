Last update: 2026-11-25 00:15 PT. Deploys loop built and run to 3 clean seeds per scenario; 4 findings (3 fixed, 1 needs a decision).

# dataplane-421-deploys

Issue #421: the data-plane chaos loop for deploy, rollback, previews and
secrets, with mgmt as replicas on Postgres.

## What's built

- **mgmt replicas in e2e/dataplane** (`mgmtpg_test.go`): `TW_DP_MGMT=pg3`
  (or `pg2`) runs mgmt as replicas on a throwaway Postgres, each behind a
  fault proxy, with a shared fake setec; the pieces come from
  `e2e/internal/mgmtpg` (PR #589, shared with `e2e/multimgmt`). The mgmt
  socket spreads connections over live replicas. `restart-mgmt` restarts
  one replica; new `pg-fault` blackholes, slows or resets one replica's
  Postgres link. Works for the existing scenarios too.
- **Workload** `testdata/deploys` (`dpd`): one Durable Object class;
  `/info` reports epoch, bundle (`BUILD`), a per-copy ID, and secret
  digests from the Worker and the Durable Object.
- **Loop** (`deploys_test.go`): three deployers (deploy, rollback naming a
  target, preview, promote), two secret writers (put/delete; one name
  shared by both; SEC_P on a preview's own name), a prober and a history
  watcher, all through different replicas; faults `deploy-kill-host`,
  `deploy-pause-host`, `deploy-cut`, `deploy-pause-takeover`,
  `deploy-cut-takeover`, `deploy-move`, `deploy-race`, `deploy-kill-mgmt`,
  plus `kill-workerd`, `restart-mgmt`, `pg-fault`. Scenarios
  `deploys-hosts`, `deploys-mgmt`, `deploys-mixed`.
- **Checkers** V1–V6 (`check_deploys_test.go`), replay
  (`TestReplayDeploys`), known-issue notes (`known_deploys_test.go`).
- `make e2e-dataplane-deploys` (deploys-mixed, 4 min, date seed) and a
  nightly matrix entry (30 min budget; a run takes ~6 min).
- Docs: LOOP.md "Deploys", `ledger/deploys.md` (+ pointer in LEDGER.md),
  docs/secrets.md "Tests", docs/locks.md Tests row, ADR-046 "Tests".

## Findings

| | Issue | Fix |
|---|---|---|
| F1 V5: a racing history read erased a rollback's `rollbackOf` | #563 | PR #596 |
| F2 V4: concurrent secret writes through two replicas lost one (a deleted secret came back) | #578 | PR #597 (multimgmt H24) |
| F3 V1: a held-up deploy lowered the workload's epoch 147 → 145 | #584 | PR #601 |
| F4 V6: a preview redeployed where promote undeployed it keeps a killed Durable Object (any undeploy + redeploy on the same host) | #577 | needs a decision (what undeploy does to DO storage); reported as a known note |
| F5 V6 (Linux, SQLite mgmt): a ready report lost to a mgmt restart is never retried; the endpoint stays `minted` | #604 | PR #607 (retry on transport errors and 5xx) |

This branch carries the three fixes (cherry-picked) so the loop runs
past them; they merge cleanly when their PRs land.

## Results (macOS, mgmt pg3)

| Scenario | Clean seeds |
|---|---|
| deploys-hosts | 2, 3, 4 |
| deploys-mgmt | 1, 2, 3 |
| deploys-mixed | 1, 3, 4 (2 clean on replay after a checker fix) |

Details and the run log: `e2e/dataplane/ledger/deploys.md`.

## Tests run

- `TW_DP_SCENARIO=deploys-* TW_DP_SEED=n go test -tags e2edataplane -run TestDataplane ./e2e/dataplane/` as above.
- `make e2e-multimgmt` on the harness move (#589): pass.
- `make lint`, `make test-pg` (store changes in #596/#597), `make e2e-locks` (#601).

## Gaps

- Wall-clock skew is not in the mix: the host skew hook
  (`dataplane-415-finish`) is not on master yet.
- Linux: one run (deploys-mixed 1, SQLite mgmt: the twd-dev VM has no
  Docker for the Postgres replicas): V1–V5 pass, V6 found F5 (#604). No
  clean Linux seeds yet.
- After merging master: `deploys-mixed` 5 (90 s) and the #415 `mixed`
  scenario with `TW_DP_MGMT=pg3` (90 s) pass; `make lint`, `make
  changes-check` ok; `go test ./docs/` ok.
- F4 (#577) needs Ross's decision.
