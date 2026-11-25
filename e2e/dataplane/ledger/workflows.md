# Ledger: Workflows and approvals (#418)

The Workflows slice of the data-plane loop (`LOOP.md`, issue #418). Same
cluster and nemesis as `TestDataplane`; its own test
(`TestDataplaneWorkflows`, `workflows_test.go`), workload
(`testdata/wf`) and checkers (`check_workflows_test.go`).

```sh
make deps
TW_DP_SCENARIO=workflows TW_DP_SEED=10 TW_DP_DURATION=3m \
  go test -tags e2edataplane -count=1 -v -timeout 25m -run TestDataplaneWorkflows ./e2e/dataplane/
```

`make e2e-dataplane` (and so the nightly) runs it after `TestDataplane`
with today's date as the seed: `workflows` on an even date,
`workflows-move` on an odd one, for 3 minutes (about 4 minutes in all).
`TW_DP_SCENARIO=workflows|workflows-move` runs it alone, and any other
scenario skips it. `TW_DP_REPLAY=<results dir> go test -tags e2edataplane
-run TestReplayWorkflows ./e2e/dataplane/` re-runs W1–W5 on a recorded run.

## The workload (`testdata/wf`)

One Workflow, `chaos`, deployed under the workload name `dp` (the name the
harness's deploy and the nemesis's moves use). Every instance runs 3
`step.do` steps (each one egress call to the recorder with a fresh call
ID; every third instance fails `s1` once), a `pre-sleep` step, a 1–5 s
`step.sleep`, a wait for event `go`, a wait for an `approval` and a
`final` step. Outside the steps, `run()` reports what it saw (the stored
results, the sleep's deadline, the event, the decision); those reports
repeat on every replay of `run()`.

The driver creates an instance every 1.5 s, sends `go` when an instance
reports it waits, and decides approvals through `POST
/_tw/approvals/chaos/<id>` from two approver nodes (every node holds
`cap/approve` for `chaos`): every 4th is rejected, every 5th is decided by
both approvers at once (approve and reject). A nudger reads the status of
an instance that has been quiet for 15 s and reacts if it waits (its
report can be lost when its host's egress is down).

## Faults

`workflows`: kill-host, crash-hostd, kill-workerd, pause-short, cut-short,
redeploy, restart-mgmt. `workflows-move`: move, pause-takeover,
cut-takeover, redeploy, kill-host. `redeploy` (new, `workflows_test.go`)
is `deploy --replace` onto the current host, alternating two builds of the
workload (`VERSION` 1 and 2).

Not yet: wall-clock skew on the host (ADR-028). The hook is being added on
branch `dataplane-415-finish` (LOOP.md "Not yet"); add it to both
scenarios once it lands.

## Invariants

| ID | Invariant |
|---|---|
| W1 | Each completed step's stored result comes back on every replay (one call ID per step across all of `run()`'s reports) and is the result of a real call; no step's side effect arrives after `run()` saw the step complete; a planned failure retries from the failed step; a complete instance's output holds the same results |
| W2 | A sleep ends no earlier than its deadline (the recorder's clock at `pre-sleep` + the sleep), and no later than the deadline + 10 s + the time faults overlapped it (each widened by 30 s). An event or approval wait ends after the first send, and no later than the first accepted send + the same allowance. A wait whose lease left the host meanwhile is noted, not bounded |
| W3 | One accepted decision per approval (one `200`, one `approval_decision` record); one `approval_requested` record per wait; the Workflow sees the accepted decision, with the accepting caller's node ID as approver (`via: tailnet`); every replay sees the same decision. A missing `approval_requested` right before a hostd crash is a note (the chain loses what it hadn't flushed; `hostd/auditresume.go`) |
| W4 | An instance acts only on the host holding its storage, and only while that host holds the lease (5 s fence slack, 10 s activation lag of the watch). After a move it is gone from the final copy (status 404). Resuming after a move back is noted (#565). Approvals still listed for a gone instance are noted |
| W5 | Every instance whose create was acknowledged (or that ran) ends `complete`, `errored` or `terminated` within 180 s of healing, or is gone because its storage is on another host |

## Status

| Scenario | Clean seeds (consecutive) | Notes |
|---|---|---|
| workflows | 10, 12, 14 (macOS); 20, 22 (Linux arm64) | with the #561 fix |
| workflows-move | 11, 13, 15, 20261125 (macOS); 21, 23 (Linux arm64) | with the #561 fix; resumptions after a move back noted (#565) |

Done (#418): 3 clean seeds per scenario, a Linux run each, the docs
(`docs/approvals.md` "Under faults" and "Moves", `docs/locks.md`, COMPAT
rows) cite the test, and it runs nightly in `make e2e-dataplane`.

## Findings

| ID | Verdict | Invariant | Issue | Summary |
|---|---|---|---|---|
| FW1 | FIXED (#571) | W3 | #561 | Two decisions for one waiting approval that reach hostd together are both accepted, delivered and audited (13 of 13 concurrent approve/reject pairs in seed 2; also one approver's request retried after a host stall, seed 1). `hostd/approvals.go` `decideApproval` released the lock between finding the pending approval and marking it decided. Fix: claim before delivery. Regression test `hostd:TestDecideApprovalOnce` |
| FW2 | NEEDS DECISION | W4 | #565 | A move leaves in-flight instances in the old host's DO storage: gone from the new copy (404, as documented now), their approvals stay listed in `tw approvals` and can't be decided, and a move back to that host resumes them (6, 7, 33 and 31 resumptions in seeds 3, 11, 15, 21). The same holds for every Durable Object, contrary to `docs/locks.md` ("never served again"; fixed in the docs). Whether a move back should start empty is Ross's call |

## Hypotheses

| ID | Status | Hypothesis | Result |
|---|---|---|---|
| HW1 | OPEN | Wall-clock skew on the host moves sleep and wait deadlines (ADR-028 says skew must not matter) | Needs the skew hook (`dataplane-415-finish`) |
| HW2 | OPEN | A decision whose delivery succeeds in workerd but reaches hostd as a failure (timeout) leaves the approval pending; a retry delivers a second `approval` event | Not seen: 0 such cases in 8 clean runs. Would need a fault between workerd and hostd during delivery |
| HW3 | REFUTED | A hostd crash around a sleep's deadline loses the wake-up (seed 1, `wf00032`, stuck `waiting`) | Its reports after the crash were lost, so the driver never sent its event (harness: the nudger). With the nudger, every instance completed in seeds 10–21, crash-hostd and kill-host included |

## Harness notes (not product bugs)

- The engine ends a run at a long wait by throwing `WorkflowAbortedError`
  and runs it again when the event arrives. The first workload caught
  errors around `waitForEvent` and reported the abort as a decision
  (seed 1). It no longer catches.
- A `crash-hostd` leaves workerd running without hostd (#432) until the
  new hostd reaps it: an instance that wakes in that window can't reach
  the recorder, so the driver never hears that it waits. The nudger reads
  quiet instances' status instead (seed 1, `wf00032`).
- The deployment watch sees a new copy only once mgmt reports it active,
  about 3 s after it starts running Workflows. W4 allows 10 s of
  activation lag (seed 3 reported 27 false stale actions without it).
- hostd's audit chain is flushed with each heartbeat; a hostd crash loses
  the unflushed tail (`hostd/auditresume.go`), including
  `approval_requested` records (seeds 2, 3, 11). Documented in
  `docs/approvals.md`.

## Run log

| Run (results dir) | Scenario / seed | Verdict |
|---|---|---|
| 20261125T062048Z-workflows-1 | workflows 1, 60 s | W3 fail: FW1 (#561); W5: harness (lost "waiting" report → nudger); workload caught `WorkflowAbortedError` (fixed) |
| 20261125T062701Z-workflows-2 | workflows 2 | W3 fail: FW1 (13 doubles), and lost `approval_requested` after crashes (now a note) |
| 20261125T062657Z-workflows-move-3 | workflows-move 3 | W3 FW1; W4: 27 false stale actions (activation lag, fixed); 6 resumptions after a move back → FW2 (#565) |
| 20261125T063741Z-workflows-10 | workflows 10 (with #561 fix) | pass |
| 20261125T063739Z-workflows-move-11 | workflows-move 11 | pass; 7 resumed after a move back, 2 approvals still listed |
| 20261125T064156Z-workflows-12 | workflows 12 | pass |
| 20261125T064158Z-workflows-move-13 | workflows-move 13 | pass; 3 resumed, 38 approvals still listed |
| Linux arm64 (lima twd-dev) | workflows 20 | pass |
| 20261125T065019Z-workflows-14 | workflows 14 | pass |
| 20261125T065022Z-workflows-move-15 | workflows-move 15 | pass; 33 resumed |
| Linux arm64 (lima twd-dev) | workflows-move 21 | pass; 31 resumed |
| make e2e-dataplane (macOS), 20261125T070429Z-workflows-move-20261125 | workflows-move 20261125 | HARNESS-BROKEN: W4 8 false stale actions (the activation-lag check looked 10 s ahead and found the next move instead of the arrival; now "did the lease arrive within 10 s"); W5 2 stuck (the nudger read statuses one by one with a 10 s timeout from a stalled copy, so it reached them only at the end; now 16 at a time, 3 s). `TestReplayWorkflows` on it: W4 clean |
| rerun after the harness fix | workflows-move 20261125 | pass; 28 resumed |
| Linux arm64, final harness | workflows 22, workflows-move 23 | pass, pass |
