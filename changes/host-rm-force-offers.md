---
kind: feature
upgrade: [protocol, cli-json, config-default]
---
`tw host rm --drain` is now `tw host rm --force`. It kills the host's
objects and releases its leases, which loses whatever they kept on that
host, so it should not share a name with `tw host drain`, which moves
workloads off without losing data. `--drain` still works for now, marked
"deprecated: same as --force". The API is unchanged
(`DELETE /v1/hosts/{id}?drain=true`).

mgmt's `lease_held` message for a workload pinned away from a live host
now says to redeploy with `--replace` (or `tw host drain <id>`), and
suggests `tw host rm <id> --force` only for a host that is gone for good.
The `takeover_unconfirmed`, `host_identity_mismatch`, `exists` and
`host_holds_leases` hints say `--force` too. An older tw CLI that reads one
of these hints doesn't know `--force` yet: use `--drain` there.

`tw host ls` calls its `TIERS` column `OFFERS` (a host offers isolation
tiers and, if it can, containers: containers are not a tier), makes it wide
enough for `process,microvm,containers`, and adds an `ARCH` column. With
`--json` each host also has `arch` and `features`. `tw status`'s host line
and the admin UI say "offers" too. The docs call the Node.js runtime the
Node runtime rather than the Node tier.
