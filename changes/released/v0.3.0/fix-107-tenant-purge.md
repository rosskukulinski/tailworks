---
kind: fix
upgrade: [schema-expand, protocol]
---
`tw tenant rm` now deletes everything the tenant owns. Before, it left the
tenant's KV, D1 and R2 journals, queue messages, workload secrets, store
and queue data keys, volume keys, notification and telemetry settings and
its files on hosts behind, where no API could reach them. Now mgmt deletes
the tenant's custody entries (the crypto-shred), then every row of the
tenant in one transaction, and every host removes `tenants/<id>/` and the
tenant's R2 blobs at its next heartbeat (audited `tenant_purge_host`). The
`tenant_delete` audit record counts what was removed. If a delete fails part
way, run `tw tenant rm` again to finish it. Deleting a tenant no longer
releases the leases of another tenant whose ID matches it as a `LIKE`
pattern (`_`).

Operator action: hosts with an S3 R2 backend log the prefix
(`t/<tenant>/`) to delete by hand; mgmt cannot list that bucket. Custody
entries written before this release by workloads that were never deployed
(secrets only) are not indexed, so a tenant deleted later can leave them.
