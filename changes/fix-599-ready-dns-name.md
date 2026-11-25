---
kind: fix
upgrade: []
---
An object no longer reports ready before it knows its MagicDNS name
(#599). A new node can be running before its first netmap carries its
own name. When that happened, the object went ready with an empty name:
mgmt recorded no MagicDNS for it, and `ctx.tailnet.self().magicDNS` was
`""` for the first seconds. Now the join waits, up to 10 s, until the
netmap shows the name. A control that sends no DNS names still joins, with
an empty name as before.
