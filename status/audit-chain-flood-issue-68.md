Last update: 2026-11-15 PT. Audit chain survives a flood (red-team H03 / fix F2, issue #68).

## What was wrong

hostd's audit queue (`hostd/audit.go`) kept at most 10 000 pending records
and trimmed the oldest when it overflowed. Those records were already
hash-chained, so the next flush began at a record whose `prevHash` mgmt had
never stored. `IngestAudit` refused that batch (`prevHash does not link to
the head of chain`) and every batch after it, so the host's chain stayed
broken until hostd restarted, and host-source records were lost. A flood of
tenant `audit()` notes, or of public requests to unlisted paths (one
`public_request` record each), while mgmt was slow or unreachable was
enough to cause it.

## What changed

- `hostd/audit.go`: the queue never removes a chained record except on a
  successful flush. The queue has three segments, oldest first: memory, a
  disk spill, and a memory tail used only if the disk refuses writes.
  Flushes go oldest first, in batches of 1000, and stop at the first error
  with nothing lost. Records that may be shed are refused *before* they are
  chained, and counted. At the next flush the counts are chained as one
  `audit_dropped` record with detail `{n, byReason, from, to}`.
  - Tenant notes are refused when over their bucket (`tenant_rate`) or while
    the queue is backed up (`queue_full`).
  - `public_request` records are refused only once the spill is at its cap
    (`spill_full`).
  - Every other host record is always chained.
- `hostd/auditspill.go`: the spill is `<data-dir>/audit-spill.jsonl`, capped
  at 256 MiB for `public_request`. Its link to the chain lives in memory,
  so records left by a previous run are discarded at start and reported as
  `audit_dropped{restart}`. Memory-queued records were already lost on a
  restart before this change.
- `hostd/auditlimit.go`: a token bucket per object on tenant notes (10/s,
  burst 200). `audit()` returns `accepted` < sent when notes are refused.
- `netd/public_serve.go`: `not_public` and `bad_request` route refusals are
  counted in the per-minute `public_rejected` summary (new `NotPublic` and
  `BadRequest` fields), not reported one by one.
- Spec: `audit_dropped` added to `AuditKind` (host chain). mgmt lists it in
  `PrivilegedKinds`. `twd audit` renders it, and renders the new summary
  counts.
- `security/LEDGER.md`: H03 fixed, C03 pinned, F2 done.
  `docs/public-ingress.md` Audit section updated.

## Tests

- `security/attacks.TestRT3_H03_AuditFloodKeepsHostRecordsAndChain` drives
  hostd's real queue (`hostd.AuditQueueForTest`) into a real
  `mgmt.IngestAudit` and checks these, in order:
  1. A batch with a hole is refused, which is the break mechanism.
  2. A flood of 3000 tenant notes, 1000 `public_request` records and 300
     `deny` records runs with mgmt away and failing heartbeat flushes.
  3. When mgmt is back, one flush drains everything and `VerifyChain` is OK.
  4. Every host record arrived.
  5. The `audit_dropped` counts equal the refused records exactly, with all
     three reasons exercised.

  With the old head-trimming put back temporarily, the test fails with
  `H03: flush after the flood: bad_request: record 0: prevHash does not link
  to the head of chain host:h1`.
- `hostd.TestAuditSpillKeepsOrder`, `TestAuditFlushFailureKeepsBatch`,
  `TestAuditSpillAfterRestart` and `TestTenantNoteBucket`.
- `netd.TestPublicRoutes` now expects route refusals in the summary.
- `cmd/twd.TestDescribePublicKinds` covers the new render cases.
- `make lint test`: see the PR.

## Gaps / next

- The spill is not resumable across a hostd restart, because the chain link
  is in memory. Persisting the head and offset would let it resume.
- Backpressure on callers was not added. Spilling covers host records, and
  the internet-driven ones are bounded by the public rate limits.
