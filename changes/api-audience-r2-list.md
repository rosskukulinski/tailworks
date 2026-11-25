---
kind: feature
upgrade: [protocol]
---
`GET /v1/tenants/{id}/workloads/{workload}/stores/r2/{name}/objects`
lists an R2 bucket's live objects (`admin|tenant:data`, paged with
`limit`/`cursor`, filtered by `prefix`). mgmt folds the bucket's index
itself, so `tw r2 object ls` no longer pages the raw journal. An index
entry written by a newer tailworks is `409 version_skew`.

The API contract (`docs/contracts/mgmt-api.md`) now has two route tables:
the operator API, and the host protocol (the routes only `tw-hostd`
calls). Role strings in `api/openapi.yaml` are in one order
(`admin|host|…`); what each role may call is unchanged.

Operator action: upgrade tw-mgmt before (or with) the CLI. `tw r2 object
ls` against an older tw-mgmt fails with `not_found` and says to upgrade
mgmt.
