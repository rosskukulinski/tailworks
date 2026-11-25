---
kind: feature
upgrade: [protocol, cli-json]
---
`tw policy --apply` and `tw deploy --apply-policy` write an app's missing
policy lines to the tailnet policy for you, so you no longer have to paste
them into the admin console. That covers the `tagOwners` of its tags, the
`tailnet.connects` grants, and `tag:tw-public` with its funnel `nodeAttrs`.
tw prints the lines first and asks before writing; pass `--yes` when there
is no terminal. mgmt then merges in only what is missing. Comments are kept,
nothing is changed or removed, and nothing outside `tag:tw*` is written.
It validates the result and writes it with `If-Match`. If the policy changed
in the meantime, it retries once, then fails with `policy_conflict`. Each
write is audited as `policy_apply`. The new route is
`POST /v1/tenants/{id}/workloads/{workload}/policy`. It is for admins only,
and `dryRun=true` gives a plan without writing. The new error codes are
`policy_conflict` and `policy_write_denied`. `tw deploy --json` gains a
`policy` event.

Operator action: to use it, the tenant's OAuth client needs the Policy File
write scope. If it doesn't have that scope, paste the snippet `tw policy`
prints, as before.
