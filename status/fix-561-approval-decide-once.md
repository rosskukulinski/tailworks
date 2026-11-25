Last update: 2026-11-24 23:05 PT. An approval is decided once (#561); PR #571.

# fix-561-approval-decide-once

Found by the e2e/dataplane Workflows loop (#418, W3): two decisions for
one waiting approval that reached hostd together were both delivered and
both audited (13 of 13 concurrent pairs in one run, and one approver's
retried request after a host stall).

## What changed

- `hostd/approvals.go`: `decideApproval` claims the approval (in-memory
  `deciding` set) after authorization and before delivery; a concurrent
  decision gets `409 approval_not_pending` ("is being decided"). A failed
  delivery drops the claim, so the approval stays pending.
- `hostd/approvals_decide_test.go` `TestDecideApprovalOnce`: deterministic
  regression test (holds the first delivery, drives a second one in).
- docs/approvals.md, docs/errors.md, docs/COMPAT.md (approvals row),
  changes/fix-561-approval-decide-once.md.

## Tests

- `go test -race -count=5 -run TestDecideApprovalOnce ./hostd/`: pass;
  fails without the fix.
- `make lint`: pass.
- `make test` on the mbp runner: load timeouts only (mgmt
  TestWorkloadReplicas, TestStoresLimits, TestTenantAdminScope; hostd
  TestR2Streaming; cmd/tw TestDevPython, TestCLIOAuth); each passes when
  rerun locally.
- e2e/dataplane `TestDataplaneWorkflows` with the fix: W3 clean on 8 seeds
  (macOS and Linux arm64).
