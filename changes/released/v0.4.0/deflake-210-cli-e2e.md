---
kind: fix
upgrade: []
---
`tw deploy` no longer ends in `identity_pending` ("not ready after 3m0s
(status active, node minted)") when it runs while a freshly started host is
still bringing up the previous deployment of the same workload, for example
right after `tw up`. The host's node joined, but its ready report still
carried the old lease epoch, and mgmt refused it with `lease_lost`. The host
now refreshes the epoch from mgmt and sends the report again.
