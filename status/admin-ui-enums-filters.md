Last update: 2026-11-25 PT. #514 and #512: admin UI enum lists from the spec; tag/human/workload filters in /v1.

## What

- #514: `mgmt/ui/enums.go` reads the `ObjectStatus` and `AuditKind` enums
  from the embedded spec once (`sync.OnceValues`, on first render of
  /ui/objects or /ui/audit, never at init: cmd/tw links mgmt/ui). States
  keep spec order; kinds are sorted. A parse failure logs and renders empty
  lists. The two hand-kept slices are gone. `transferred` gets the grey
  badge. `TestEnumListsMatchSpec` pins both lists to the spec.
- #512: `GET /v1/objects?tag=&human=` and `GET /v1/audit?workload=`.
  - `tag`: exact element match on the JSON `tags` text (`instr` / `strpos`
    of the JSON-encoded tag, quotes included), both dialects.
  - `human`: `acting_for=?`, exact.
  - `workload` on audit: new side table `audit_workloads (chain, seq,
    workload)` written in `appendAuditTx` (object's workload, else
    `detail.workload` of mgmt-chain records; host/tenant detail never
    trusted). Not an audit column because audit rows are append-only (the
    UPDATE trigger would block a backfill). Migration
    `202611251900_audit_workload` creates and backfills it; retention
    deletes its rows with their records.
  - UI: both pages pass the filters through. Objects page keeps
    `objectsScanMax`/`Truncated` (ListObjects has no newest-first order),
    with a generic note; audit page loses `Hidden`, `detailWorkload` and the
    hint.

## Tests

- `go test ./mgmt/... ./hostd/fakemgmt/`: ok
- `make test-pg` (Postgres 17, OrbStack): ok, incl. the new
  `TestStoreAuditWorkloadAndObjectFilters/postgres` (backfill SQL on PG)
- `make lint`: ok (after merging origin/master)
- `make test` (mbp, after merging origin/master): ok
- `make e2e-multimgmt`: 9 scenarios failed only from a concurrent run on
  the same Docker (`tw-mm-pg` name conflict, shared store); rerun alone,
  all 9 pass.

## Gaps

- Rolling upgrade: records written by an N-1 replica have no
  `audit_workloads` row (noted in the changes fragment).
- `tw ls --tag/--human` and `tw audit --workload` not added (optional per
  the issue).
