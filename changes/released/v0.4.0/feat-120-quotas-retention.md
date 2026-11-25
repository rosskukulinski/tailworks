---
kind: feature
upgrade: [schema-expand, protocol]
---
Per-tenant storage quotas, usage, disk checks and retention (#120).

- `tw tenant set --max-store-bytes 20GiB --max-queue-bytes 2GiB --max-stores 50`
  bounds what one tenant keeps in mgmt: all its KV, D1 and R2 journals, all
  its queued messages, and how many stores it has. A write past a quota fails
  `429 storage_quota_exceeded`; deletes and overwrites that don't grow a store
  still work. The quotas are unlimited unless set (`-` clears one).
- `tw tenant ls` shows each tenant's storage usage against its quotas
  (`GET /v1/tenants/{id}/usage`, which also lists its largest stores and queues).
- `tw doctor` checks the free space on the state dir's volume (`data_disk`) and
  warns when a store, queue or tenant quota is 80% full (`store_sizes`).
- Audit retention, off by default: `tw up --audit-max-age 2160h` or
  `--audit-max-records N` (`tw-mgmt -audit-max-age`, `-audit-max-records`)
  deletes each chain's oldest records and keeps a checkpoint, so what remains
  still verifies (`prunedThrough` in `GET /v1/audit/verification`). Each prune
  is audited (`audit_pruned`).
- Request records, off by default: `tw up --request-log-max-age 168h`
  (`tw-hostd -request-log-max-age`) drops request records older than that.
- `tw up`'s `mgmt.log` and `hostd.log` now rotate at 32 MiB (one previous file kept).

Operator action: none to upgrade. Two new migrations add three `tenants`
columns and the `audit_checkpoints` table. The audit table's append-only
guard now lets the pruner delete records its checkpoint covers; while an
older tw-mgmt replica is still running against the same Postgres store, its
start reinstalls the older guard and pruning waits for the next pass after
the upgrade finishes.
