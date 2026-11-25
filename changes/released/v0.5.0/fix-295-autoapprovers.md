---
kind: feature
upgrade: [protocol]
---
`tw policy --apply` and `tw deploy --apply-policy` now write the
`autoApprovers.services` entry a workload with `tailnet.service` needs, so
you no longer paste it into the policy by hand. The entry is as narrow as
it gets: only the workload's own `svc:` name, approved only for the tags
its node is deployed with, and only `tag:tw*` tags (`policy.Check`
refuses anything else, and the merge never changes existing
`autoApprovers` entries, routes or exit nodes). On Headscale, or when the
workload's tag isn't defined in `tagOwners`, the entry stays in the
"add it yourself" list. The `api/openapi.yaml` change is to the
description of `POST /v1/tenants/{id}/workloads/{workload}/policy` only:
no field was added or removed.
