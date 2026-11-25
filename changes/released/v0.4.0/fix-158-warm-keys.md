---
kind: security
upgrade: [protocol, config-default, cli-json]
---
Warm keys (pre-minted, preauthorized single-use join keys that make new
objects start faster) no longer outlive the mgmt that holds them. `tw down`
now revokes every unused warm key with Tailscale and deletes it from mgmt's
custody as mgmt stops (before, only `tw down --purge` did; otherwise they
stayed in custody until they expired, up to 30 min). `tw tenant rm` already
revoked them. `tw status` shows how many warm keys each tenant has in
custody, and `GET /v1/tenants/{id}` has a new `warmKeys` count (also in
`tw status --json`).

Two narrow leaks are closed too: a warm key being minted when mgmt shut
down, and one minted while `tw tenant rm` ran, could stay live at Tailscale
until it expired. Both are now revoked.

tw-mgmt has a new flag, `-revoke-warm-keys-on-exit` (off by default; `tw up`
sets it). Leave it off for mgmt replicas that share a store, or every
replica restart revokes the shared pool.

Operator action: none. After a `tw up` restart (including `tw upgrade`) the
pool refills on the next create or deploy.
