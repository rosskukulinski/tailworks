Last update: 2026-11-24 23:10 PT. Workflows/approvals chaos loop (#418) built; 3 clean seeds per scenario; findings #561 (fixed, PR #571) and #565 (needs a decision).

# dataplane-418-workflows

The Workflows and approvals slice of the data-plane chaos loop (#418, on
the #415 harness). Ledger: `e2e/dataplane/ledger/workflows.md`.

## What's built

- `e2e/dataplane/testdata/wf`: one Workflow (`chaos`) with 3 steps (one
  egress call each, a planned failure in every third instance), a sleep,
  an event wait, an approval wait and a final step; `run()` reports what it
  saw on every replay.
- `e2e/dataplane/workflows_test.go`: `TestDataplaneWorkflows` (cluster as
  `TestDataplane`, workload deployed as `dp`), the driver (creates,
  events, approvals from two approver nodes, concurrent double decisions,
  a nudger for lost reports), the recorder, and the `redeploy` fault.
- `e2e/dataplane/check_workflows_test.go`: W1–W5.
- Scenarios `workflows` and `workflows-move` (`nemesis_test.go`), and
  `TestDataplane` skips them. `make e2e-dataplane` runs the Workflows test
  after `TestDataplane` (3 m, `workflows` on an even seed,
  `workflows-move` on an odd one), so the nightly covers it.
- Docs: `docs/approvals.md` "Under faults" and "Moves" (the answer to
  #418's open question), `docs/locks.md` (storage comes back on a move
  back), COMPAT Workflows and approvals rows cite the test.

## Findings

- #561 (fixed in PR #571, merged into this branch): two concurrent
  decisions for one approval were both accepted and delivered.
- #565 (needs Ross's decision): after a move, in-flight instances are gone
  from the new copy (documented), their approvals stay listed and can't be
  decided, and a move back to the old host resumes them (all Durable
  Object storage comes back, contrary to docs/locks.md, now corrected).
- Not a bug, documented: a hostd crash loses the unflushed tail of its
  audit chain, `approval_requested` included.

## Tests

- Chaos runs (`TW_DP_SCENARIO=<s> TW_DP_SEED=<n> go test -tags
  e2edataplane -run TestDataplaneWorkflows ./e2e/dataplane/`, 3 m):
  `workflows` 10, 12, 14 (macOS), 20 (Linux arm64, lima twd-dev): pass.
  `workflows-move` 11, 13, 15 (macOS), 21 (Linux arm64): pass. Seeds 1–3
  (before the #561 fix and three harness fixes) failed: see the ledger.
- Final harness: `workflows-move` 20261125 (macOS, the nightly's seed),
  `workflows` 22 and `workflows-move` 23 (Linux arm64): pass.
- `make e2e-dataplane` (macOS): `TestDataplane` mixed 20261125 pass;
  `TestDataplaneWorkflows` workflows-move 20261125 failed on two harness
  bugs (W4 activation-lag lookahead, a slow nudger), fixed; replay and rerun
  of the seed pass. The target takes about 10 minutes.
- `make lint`: pass. `make test`: `test-js` pass; `test-go` pass except a
  nil-pointer panic in the `hostd/connect_test.go` `serveWhoisTCP` test
  helper (unrelated); `go test -race ./hostd/` rerun: pass.
- `make changes-check`: ok (no fragment needed).

## Gaps

- Wall-clock skew on the host: waits for the hook from
  `dataplane-415-finish` (ledger HW1).
- Approval timeouts (the wait's `timeout`) are not exercised (15 min).
