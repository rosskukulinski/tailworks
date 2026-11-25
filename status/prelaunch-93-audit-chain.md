Last update: 2026-11-17 PT. Audit chain verification no longer fails on an intact chain under concurrent appends (#93).

## What was wrong

`mgmt.VerifyChain` read a chain's records (`ListAudit`) and its head
(`AuditHead`) in two separate, non-transactional queries. Appends are
atomic (record insert and head update in one transaction), but one that
committed between the two reads left the head one record ahead of the
list, and verification failed with `chain head mismatch` at
`BrokenAt = lastSeq+1` on an intact chain. `TestCLIMultiTenant` hit it in
CI (for example run 43891095144: `host:twd-m`, 15 records, brokenAt 16)
because hostd keeps pushing records while `tw audit --all` verifies, and
the CLI then exits with `audit_unverified`. It was quarantined in
`ci.yml` with `GO_TEST_SKIP`.

## What changed

- `mgmt/auditsnapshot.go` (new): `SQLStore.AuditChainSnapshot` reads the
  head and the records in one read transaction (Postgres: `REPEATABLE
  READ`, read-only; SQLite: the store's single connection is held by the
  transaction, so no append can interleave).
- `mgmt/store.go`: `AuditChainSnapshot` added to `Store`.
- `mgmt/sqlstore.go`: `ListAudit` delegates to `listAudit(ctx, queryer, f)`
  so the same query runs on the pool or in a transaction. No query change.
- `mgmt/audit.go`: `VerifyChain` uses the snapshot. Truncation, gap,
  modification, relink and out-of-band-append detection are unchanged
  (the head check is as strict as before, just consistent).
- `mgmt/auditsnapshot_test.go` (new): `TestAuditVerifyConcurrentAppend`,
  an appender goroutine (300 appends) against a verify loop, on SQLite and
  (with `TW_TEST_POSTGRES_DSN`) Postgres. It fails on the old
  `VerifyChain` on the first or second verify, every run.
- `.github/workflows/ci.yml`: removed the `TestCLIMultiTenant` quarantine.

## Tests

- `go test -race -count=3 -run 'TestAuditVerifyConcurrentAppend|TestAuditChain' ./mgmt/`
  with `TW_TEST_POSTGRES_DSN` (Postgres 17): pass on SQLite and Postgres,
  3/3 (75-102 verifies on SQLite, 234-285 on Postgres, during 300 appends).
- Same test against master's `mgmt/audit.go`: fails on both backends
  (`chain head mismatch`, BrokenAt = lastSeq+1).
- `go test -race -count=4 -run '^TestCLIMultiTenant$' ./cmd/tw/`: 4/4 pass.
- `make lint`: pass. `make test`: pass (includes TestCLIMultiTenant).
- `make test-pg` equivalent (`scripts/test-pg.sh` with a renamed
  container/port so it can't clash with other worktrees): store phase ok.
  The API phase had one `TestWarmKeyExpiry` failure ("2 revoke records for
  3 expired keys") on a heavily loaded machine. It passed 4/4 when rerun
  alone on Postgres. It counts records with `ListAudit`, and this change
  doesn't alter what that query returns.

## Not changed / follow-up

- `IngestAudit` reads the head and then appends in separate transactions.
  Two concurrent ingests for the same host could both pass the head check;
  the result is a visible `prevHash` break reported by verification, not a
  silent loss. hostd flushes one batch at a time, so this was not the CI
  failure. Not addressed here.
