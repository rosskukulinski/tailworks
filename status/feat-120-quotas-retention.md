Last update: 2026-11-21 PT. Per-tenant storage quotas and usage, tw doctor disk and store checks, opt-in audit and request-record retention, rotated daemon logs (#120, second half).

# feat-120-quotas-retention

Refs #120. The other half of the issue (`tw backup`, the SQLite → Postgres
migration, the HA flags of `tw up`) is a separate branch.

## What changed

- **Per-tenant storage quotas** (`mgmt/quota.go`, `mgmt/sqlstore_quota.go`).
  Three tenant fields, `maxStoreBytes` (all the tenant's KV, D1 and R2
  journals together), `maxQueueBytes` (all its queued messages) and
  `maxStores` (how many platform stores). **Default 0 = unlimited**, so an
  upgrade never starts refusing an existing tenant's writes; the 1 GiB per
  store and per queue still apply. Enforced inside the append's / send's
  transaction: an append that grows its store past `maxStoreBytes`, or a send
  past `maxQueueBytes`, is `429 storage_quota_exceeded` with
  `missing: {quota, limit, used, requested}`. An append that doesn't grow its
  store (overwrite, delete) always passes, so a full tenant can free space.
  `maxStores` is checked when a store is created (PUT or first append). On
  Postgres two concurrent writes to different stores can each pass, so a
  tenant can overshoot by at most one in-flight write per store/queue
  (documented in limits.md). hostd passes the code through (`storeErrStatus`,
  `queueErrStatus`; KV maps it to 413 like `store_limit`).
- **API**: `TenantPatch` takes the three fields (`null` or 0 = unlimited);
  `GET /v1/tenants/{id}/usage` (`admin|tenant:read`) returns counts and bytes
  of stores and queues, the quotas and fixed limits, and the 5 largest stores
  and queues. Kept off `GET /v1/tenants/{id}` so the tenant's ETag doesn't
  change on every write. New error code in the spec and `docs/errors.md`.
- **CLI**: `tw tenant set --max-store-bytes 20GiB --max-queue-bytes 2GiB
  --max-stores 50` (sizes with B/KB/KiB/…/TiB; `-` clears);
  `tw tenant ls` has a STORAGE part (`stores 512 B / 1.0 KiB (50%) (1/3) ·
  queues 0 (0 B / ∞)`) and `usage` in `--json` (`cmd/tw/tenant_quota.go`).
- **`tw doctor`** (`cmd/tw/doctor_storage.go`): `data_disk` (free space on
  the state dir's volume: warn under 5 GiB or 10%, fail under 1 GiB; shows
  mgmt.db's size) and `store_sizes` (warn when any store or queue is 80% of
  its 1 GiB, or a tenant 80% of a quota).
- **Audit retention** (`mgmt/retention.go`, `mgmt/sqlstore_retention.go`).
  **Off by default: the audit log is kept forever**; opt in with
  `tw up --audit-max-age` / `--audit-max-records` (saved in tw.json, kept by a
  later `tw up`) or `tw-mgmt -audit-max-age` / `-audit-max-records`. Choice:
  checkpointing, so pruning keeps the chain verifiable. The pruner (hourly and
  at start, 5000 records per transaction) writes the chain's checkpoint (seq
  and hash of the last record deleted, cumulative count) in
  `audit_checkpoints`, then deletes, in one transaction holding the chain's
  head row lock. `VerifyChain` starts from the checkpoint: records kept must
  start at checkpoint+1 and link to its hash; edits, gaps and a truncated tail
  are still caught. `ChainStatus.prunedThrough` reports it, `tw audit` prints
  it, every prune is an `audit_pruned` record on the mgmt chain. The
  append-only guard (SQLite triggers, Postgres trigger function, baseline DDL
  in `sqlstore.go`) now allows exactly one DELETE: of records a checkpoint
  covers; everything else is still refused. The age rule stops at the first
  record that isn't old enough (a skewed host timestamp keeps everything after
  it, never skipped).
- **Request record retention** (`hostd/telemetry_retention.go`,
  `internal/obs/files_prune.go`): `tw up --request-log-max-age` /
  `tw-hostd -request-log-max-age` drops request records older than that from
  every tenant's request logs (open or not), every 10 min. Off by default
  (size rotation only, as before).
- **Daemon logs**: `tw up`'s `mgmt.log` and `hostd.log` now rotate at 32 MiB
  with one previous file (`cmd/tw/up_logs.go`), in both layouts.
- **Migrations**: `202611212130_tenant_storage_quotas` (3 tenant columns,
  default 0) and `202611212131_audit_checkpoints` (table). Expand only.
- **Docs**: limits.md (new "Tenant storage" section, a Queues row, rows and
  an "Audit retention" note under Logs), errors.md, INSTALL.md ("Disk,
  storage quotas and retention"), observability.md volume bounds,
  docs/cli.md, mgmt-api.md (generated), changes fragment.

## Tests

- New: `mgmt` `TestTenantStoreQuota`, `TestTenantMaxStores`,
  `TestTenantQueueQuota` (enforcement, overwrite at quota, clear, usage
  endpoint), `TestAuditPruneKeepsChainVerifiable` (SQLite and Postgres:
  batches, checkpoint, appends after a prune, guard still refuses other
  deletes, tamper and gap after a prune still caught), `TestAuditRetentionPruner`
  (pruner, `audit_pruned` record, `/v1/audit/verification`, off by default);
  `cmd/tw` `TestCLITenantQuotas` (set/clear, `tenant ls` usage, typed
  error), `TestParseByteSize`, `TestDoctorStorage`, `TestUpRetention`;
  `hostd` `TestRecordLogMaxAge`; `internal/obs` `TestRotatingDropBefore`.
- `make lint`: ok.
- `make test-pg` (local OrbStack Postgres 17): ok, including the new
  audit-retention store test under Postgres (the changed trigger function).
- `make test` (local; both mbp slots were busy): ok. The first run failed
  `TestDoctor` (its expected check list lacked the new checks); fixed.
- `make e2e-queues`, `make e2e-observability`, `make e2e-upgrade`: ok.

## Gaps and notes

- Rolling upgrade on a shared Postgres store: an older tw-mgmt replica's
  start reinstalls the older (strict) trigger function, so pruning fails
  (logged, retried hourly) until every replica runs this version.
- `tw logs -f` on a component log keeps reading the old file after it
  rotates (same as workerd.log before); restart the follow.
- The store quota adds one tenant-row read per journal append.
- No quota on R2 object bytes (blobs live on hosts); only the bucket index
  journal counts, as for the per-store limit.
