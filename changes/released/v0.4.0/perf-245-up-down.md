---
kind: fix
upgrade: []
---
`tw down` and `systemctl --user restart tw` no longer take 5 s extra: mgmt
stopped its node while the HTTPS certificate prefetch was still waiting on
the node, and the node waited out a 5 s grace for it. `tw up` no longer
waits for its first network check and a relay (DERP) connection before
mgmt serves: it is ready once mgmt's node is logged in. On a Linux VM,
`tw up --service` went from 3.5 s to 0.5 s and `tw down --service` from
5.1 s to 0.25 s. tw also keeps serving the local CLI right after a restart
while the relay is unreachable, for example during a boot before the
network is up. On `tw down`, mgmt revokes its unused warm keys 8 at a time
instead of one by one.
