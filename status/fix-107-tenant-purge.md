Last update: 2026-11-17 18:40 PT. `tw tenant rm` deletes everything the tenant owns (issue #107).

# fix-107-tenant-purge

## What's built

- **mgmt (`mgmt/tenantpurge.go`, `mgmt/sqlstore_tenantpurge.go`).**
  `Mgmt.DeleteTenant`, after the existing kill / purge / stop steps:
  1. revokes the tenant's unused warm keys while its credential is there;
  2. deletes every custody entry of the tenant: store and queue data keys
     (the crypto-shred `DeleteStore`/`DeleteQueue` promise), workload
     secrets and volume keys, objects' and pool auth keys, notification and
     telemetry settings, OBO roots, and every name in `custody_names`.
     Names come from the tenant's rows plus the new index; a failure stops
     here with the tenant intact (rerun to finish);
  3. one transaction (`SQLStore.PurgeTenant`) deletes `store_entries`,
     `stores`, `queue_messages`, `queues`, the objects' `revocations`,
     `objects`, `deployments`, `workload_public`, `public_names`,
     `container_reservations`, `warm_keys`, `host_paths`,
     `tenant_throttle`, `custody_names`, the OBO `secret_choice`, leases,
     registry links/tags and the tenant row, and inserts a `tenant_purges`
     row per registered host. Custody names indexed during the delete are
     returned and deleted after.
  The `tenant_delete` audit record and `TenantDeleteResult` carry
  `removed` counts. `Store.DeleteTenant` now runs the same purge.
- **Custody index.** `indexedCustody` wraps the configured custody in
  `New`: a `Put` of `tw/<tenant>/…` first records the name in
  `custody_names` (so a secret of a workload never deployed is still
  found); `Delete` removes it. `CustodyFingerprint` unwraps it.
- **Hosts (`hostd/tenantpurge.go`).** The heartbeat reply lists
  `purgeTenants` (pending `tenant_purges` rows of that host). hostd kills
  the tenant's objects, undeploys its workloads, closes its netd registry,
  removes `tenants/<id>/` and the R2 FS blobs `r2/t/<id>/`, then reports
  `purgedTenants`; mgmt deletes the row and audits `tenant_purge_host`.
  Idempotent; retried on the next heartbeat after an error.
- **Bug fixed on the way.** The lease delete used `LIKE '<id>/%'`, so `_`
  in a tenant ID matched other tenants' leases; it is a prefix compare now.
- Migration `202611180234_tenant_purge.sql` (expand only): `tenant_purges`,
  `custody_names`.
- API: `HostStatus.purgedTenants`, `HostStatusResponse.purgeTenants`,
  `TenantDeleteResult.removed`, audit kind `tenant_purge_host`; `make api`.
- CLI prompt text of `tw tenant rm`; `docs/teams.md` "Removing a team";
  `changes/fix-107-tenant-purge.md`.

## Tests

- `TestDeleteTenantPurgesAPI` (mgmt): two tenants with KV/D1/R2 journals,
  queue messages, secrets (one workload never deployed), volume key,
  notifications, telemetry and an object with a credential. Delete fails
  with custody down (tenant intact), the rerun finishes; then no row of the
  tenant in any table with a `tenant_id` column (found from the schema,
  audit excepted) or the incarnation-keyed tables, leases, secret choice;
  no custody entry or file; the other tenant unchanged; audit counts; the
  host is told to purge until it reports; a second delete is 404.
- `TestPurgeDeletedTenant` (hostd): files of the deleted tenant (node
  state, DO storage, logs, R2 blobs) gone, another tenant's kept, reports
  repeat until acked, unsafe IDs refused.
- `TestCustodyTenant`.

Commands run: see the PR's Tests section.

## Gaps

- S3 R2 backends: hostd cannot list the bucket; it logs the `t/<tenant>/`
  prefix for the operator.
- Custody entries written before this release under a workload that has no
  row anywhere (secrets of a never-deployed workload) are not indexed.
- Registry blobs stay (content-addressed, possibly shared); links and tags
  go as before. Audit records stay by design.
- The microVM helper's own state dir (`/var/lib/tailworks-vm`) is not
  touched; the tenant's DO volume images live under `tenants/<id>/`.
