Last update: 2026-11-24 15:00 PT. #377 fixed: a clean hostd restart continues the host audit chain.

# fix-377-audit-purge

## Root cause

Not the purge. hostd keeps its audit chain link (`auditLog.prev`) only in
memory, so every hostd restart began a new segment at `prevHash: ""`.
mgmt's `IngestAudit` stores such a restart (it never splices it into
history) and `VerifyChain` reports the chain BROKEN at the first record
after it. `tw down --purge` + `tw up` just guaranteed a restart followed by
host records (the kill of the new object). Reproduced hermetically without
purge, `--ephemeral`, undeploy or all-in-one: real mgmt + real hostd,
create/kill an object, `Env.Restart()`, create/kill another → `host:h1
BROKEN at 2`. Lease epochs ("credential … epoch 2") are unrelated.

## Fix

- hostd `Close`, after its final flush, writes the last chained hash to
  `<data>/audit-head` only if nothing is queued or refused-but-unreported;
  otherwise it removes any such file (`hostd/auditresume.go`).
- On start hostd reads and deletes the file. At its first registration it
  links to the saved hash only if mgmt's stored head (new `auditHead` in the
  `PUT /v1/hosts/{id}` answer, `mgmt/hostauditresume.go`) is the same hash
  and nothing has been chained yet. Anything else (crash, records lost,
  chain truncated/rewritten at mgmt, old mgmt) starts a new segment as
  before, which verification still reports.
- fakemgmt answers `auditHead` too (parity).
- `auditHead` is left out of the register answer's ETag (it is not part of
  the host record GET returns).

## Tests

- `go test ./hostd -run 'TestAuditChainAcrossRestart|TestAuditResume' -count=1`:
  pass. `TestAuditChainAcrossRestart/clean_restart` failed before the fix
  (`BrokenAt:2 prevHash does not link`); the mismatch and no-saved-head
  subtests check a break is still reported at the first record after the
  restart.
- `go test ./hostd/mgmtapi ./internal/inproc ./mgmt -count=1`: pass.
- `make lint`, `make test`: see the PR.

No mgmt store or migration change, so `make test-pg` is not needed.
