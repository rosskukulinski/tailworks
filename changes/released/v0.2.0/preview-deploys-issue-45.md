---
kind: feature
upgrade: [protocol, cli-json]
---
`tw deploy --preview` runs a bundle next to the workload instead of
replacing it, as the workload `<workload>--preview-<alias>` (the alias is
`--preview-alias`, or the start of the bundle's SHA-256). A preview has the
workload's tags and secrets, but its own lease, Durable Object storage and
KV, R2 and D1 stores, which start empty; it joins no Tailscale Service, has
no public routes and runs one replica. Bundles that bind queues can't be
previewed (queues are tenant-wide). `tw previews <workload>` lists the
running previews. `tw promote <workload> <alias>` deploys the preview's
bundle to the workload through the same path as `tw deploy` (lease epoch,
host fencing, preflight), audited as `kind: promote`, then undeploys the
preview (`--keep-preview` keeps it). The preview's data is not copied
(ADR-048).

New API: `POST /v1/deployments` `preview` and `previewAlias`,
`GET /v1/tenants/{id}/workloads/{workload}/previews` and
`POST /v1/tenants/{id}/workloads/{workload}/promotions`.
