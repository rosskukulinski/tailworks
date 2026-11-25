# Data-plane loop ledger: deploys, rollback and secrets

Issue #421. Process: `../LOOP.md` ("Deploys"). Findings are filed as
GitHub issues labelled `dataplane-chaos`. mgmt runs as 3 replicas on
Postgres (`TW_DP_MGMT=pg3`) unless a row says otherwise.

## Status

| Scenario | Clean seeds (consecutive) | Notes |
|---|---|---|
| deploys-hosts | 2, 3, 4 | seed 1 found F2 and F4 (fixed / known since) |
| deploys-mgmt | 1, 2, 3 | seed 1's first run found F3 |
| deploys-mixed | 1, 2 (on replay after the checker fix), 3, 4 | |

The issue's stop condition is met on macOS with mgmt on Postgres: 3
consecutive clean seeds per scenario. Nightly runs `deploys-mixed` with the
date as the seed (`make e2e-dataplane-deploys`).

Runs carry the fixes for F1 (#563), F2 (#578) and F3 (#584) until those
PRs land. F4 (#577) is open and needs a decision: the checkers report it
as a note naming the issue (`known_deploys_test.go`), not a failure.

## Findings

| ID | Verdict | Invariant | Issue | Summary |
|---|---|---|---|---|
| F1 | FIXED (#563) | V5 | #563 | A history read racing a rollback erased its `rollbackOf` for good: `fillRecord` backfilled the hash with the empty `rollbackOf` it had read, after `afterDeploy` had written it. `SetDeploymentMeta` no longer overwrites a recorded value with an empty one. `TestStoreSetDeploymentMetaKeepsRollbackOf` |
| F2 | FIXED (#578) | V4 | #578 | Secret writes of one workload through two replicas lost one: a `DELETE SEC_C` (r1) and a `PUT SEC_B` (r2) 1 ms apart, both 2xx, and the next deployment got SEC_C back. Read-modify-write of one custody entry under a per-process mutex. Now a Postgres advisory lock per workload. `TestMM_H24_SecretWritesAcrossReplicas`, `TestStoreLockName` |
| F3 | FIXED (#584) | V1 | #584 | A deploy of epoch 145 held up 8 s (its bundle fetch went through a replica whose Postgres link was blackholed) finished after the epoch-147 assignment and set the workload's epoch back to 145: one copy answered at 147, then 145. Fencing tokens are built on that epoch. `deploy` now only raises it. `TestDeployNeverLowersEpoch` |
| F4 | CONFIRMED, needs a decision (#577) | V6 (and V3 before the workload reported Durable Object errors) | #577 | A preview deployed again on a host where promote had undeployed it: the workload node answers, the deployment is `active`, but its Durable Object answers `410 killed` until the preview lands on another host. A kill writes `__tw_killed` into the object's storage, and DO storage outlives `tw undeploy`. Same for any workload undeployed and deployed again on the same host. Also: such a preview reuses the old storage, though ADR-048 says it starts empty |

| F5 | FIXED in PR #607 (Linux, SQLite mgmt) | V6 | #604 | A ready report lost to a mgmt restart is never sent again: the node joined at 00:13:27.73, mgmt had been SIGKILLed 70 ms earlier, the PATCH failed with `connection reset by peer`, and the deployment's endpoint stayed `minted` past the end of the run. hostd retries a ready report only after `lease_lost` (#210) |

## Hypotheses

| ID | Status | Hypothesis | Result |
|---|---|---|---|
| H1 | OBSERVED, benign | During a redeploy on the same host, the previous copy answers at the new epoch (the epoch is the workload's on the host, not the copy's) | 16–61 handover answers per run, always before the new copy's first answer (V3 never saw an older copy after a newer one on a host). The lock template's counter lives in storage both copies share, so tokens stay monotonic |
| H2 | OPEN | Wall-clock skew on a host doesn't change V1–V6 | Needs the host skew hook (`dataplane-415-finish`, not on master yet) |
| H3 | OPEN | A replica killed between `CreateDeployment` and `afterDeploy` leaves a record without its hash or `rollbackOf` | `deploy-kill-mgmt` SIGKILLs 0–300 ms after the request; every kill so far landed after the 202 (the deploy takes ~25 ms). V5 counts "hashes written late": 1–2 per run, all filled by the same request |

## Harness notes (not product bugs)

- Endpoint hosts come from the node name (`<host>-…`): a deployment's view
  can still name the previous host's node for a moment after a move (V2
  false positives in the first run).
- mgmt stamps `createdAt` with the store's clock. Postgres runs in a
  container VM whose clock was ~2.5 s ahead of the test's, so a copy
  "answered before its deployment existed". Creation times are now the
  creating op's send time (or the stamp shifted by the median offset).
- An answer's epoch doesn't identify the copy (H1), and rollbacks reuse
  bundles: every deploy now uploads a bundle of its own (`BUILD` =
  `b2.17`), and the workload reports a random ID per Durable Object copy.
- The Worker returned 200 with the Durable Object's error body inside;
  it now answers 502 with the error (this is how F4 showed its cause).

## Run log

| Run (results dir) | Scenario / seed | Verdict |
|---|---|---|
| 20261125T062734Z-deploys-mixed-1 | deploys-mixed 1, 90 s smoke | V5: F1. V2/V4 harness (endpoint host, handover) |
| 20261125T064635Z-deploys-mixed-1 | deploys-mixed 1, 90 s, with F1's fix | pass |
| 20261125T064946Z-deploys-hosts-1 | deploys-hosts 1 | V6/V3: F4. V3/V4 harness (copy identity) |
| …-deploys-hosts-1 (rerun, 23:00 PT) | deploys-hosts 1 | V4: F2. F4 again (821 answers `410 killed`) |
| 20261125T071204Z-deploys-mgmt-1 | deploys-mgmt 1 | V1: F3. V3/V4 harness (store clock). F4 |
| …-deploys-mgmt-1 (rerun, 23:22 PT) | deploys-mgmt 1, F1–F3 fixed | pass (F4 as a note) |
| …-deploys-hosts-2 | deploys-hosts 2 | pass (F4 note) |
| …-deploys-mixed-1 | deploys-mixed 1 | pass |
| …-deploys-hosts-3 | deploys-hosts 3 | pass |
| …-deploys-mgmt-2 | deploys-mgmt 2 | pass |
| …-deploys-mixed-2 | deploys-mixed 2 | V3 false positive: one answer at epoch 0 (`self()` failed while a moved copy's object was not ready; the lock template refuses then). Epoch-0 answers are now counted, not judged. Clean on replay |
| …-deploys-mgmt-3 | deploys-mgmt 3 | pass |
| …-deploys-mixed-3 | deploys-mixed 3 | pass |
| …-deploys-hosts-4 | deploys-hosts 4 | pass |
| …-deploys-mixed-4 | deploys-mixed 4 | pass |
| …-deploys-mixed-5 | deploys-mixed 5, 90 s, after merging master (workflows loop) | pass |
| …-mixed-5 (`TW_DP_MGMT=pg3`) | the #415 `mixed` scenario on 3 replicas, 90 s | pass (D1–D7) |
| …-deploys-mgmt-6 | deploys-mgmt 6, 2 min, after merging master (the R2 loop: the Postgres proxy is now `e2e/chaoskit`'s) | pass |
| twd-dev VM 20261125T080908Z-deploys-mixed-1 | deploys-mixed 1, Linux arm64, `TW_DP_MGMT=sqlite` (no Docker in the VM) | V1–V5 pass. V6: F5 (#604) |
