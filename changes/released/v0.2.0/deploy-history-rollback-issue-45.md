---
kind: feature
upgrade: [schema-expand, protocol, cli-json, config-default]
---
`tw deployments <workload>` lists a workload's deployment history, newest
first: id, created, who deployed, bundle SHA-256, status, error and host.
`tw rollback <workload> [<deployment>]` redeploys an earlier deployment's
stored bundle as a new deployment, through the same path as `tw deploy`
(placement, lease epoch, host fencing, preflight); with no ID it goes back
to the previous deployment that ran. It is audited as `kind: rollback`. A
rollback changes code and configuration only: Durable Object storage, D1
(migrations are forward-only), KV, R2 and queues keep their data, and
secrets are the workload's current ones (ADR-046).

mgmt now keeps the bundles of each workload's newest 10 deployments and
prunes older ones, keeping their record and hash (tw-mgmt
`-bundle-retention`, `-1` keeps every bundle). New API:
`GET /v1/tenants/{id}/workloads/{workload}/deployments` and
`POST /v1/tenants/{id}/workloads/{workload}/rollbacks`.

Operator action: none. The store migration adds columns to `deployments`.
The first deploy of each workload after the upgrade prunes that workload's
bundles beyond the newest 10; pass `-bundle-retention -1` to tw-mgmt
beforehand to keep them all.
