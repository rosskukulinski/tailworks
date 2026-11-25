---
kind: feature
upgrade: [protocol]
---
On a terminal, plain `tw up`, `tw deploy` and (for a Headscale tenant)
`tw tenant add` now offer to write the tailnet policy lines tailworks
needs (a Tailscale tenant's policy is still never written): they show the lines and
ask `[y/N]`, as `--apply-policy` does. They ask only when the control
plane would accept the write (an OAuth client with Policy File › Read is
never asked). "n" goes on as before: `tw up` prints the lines (and still
stops with `policy_missing_tag_owners` when `tag:tw` doesn't own
`tag:tw-mgmt`, or on Headscale), `tw deploy` fails with `acl_denied` and
the snippet. The write itself is unchanged (additive, validated, If-Match,
audited).

Without a terminal, with `--json`, or with a bare `--yes`, nothing asks and
nothing is written, as before: automation keeps `--apply-policy --yes`.
`--apply-policy=false` never asks and never writes. mgmt's policy plan
(`POST /v1/tenants/{id}/workloads/{workload}/policy?dryRun=true`) gains
`canWrite`; with an older mgmt, `tw deploy` doesn't ask.
