---
kind: fix
upgrade: [protocol]
---
`tw audit` no longer reports a host's chain as broken after a clean restart
(#377). Every `tw-hostd` restart, including `tw down [--purge]` and `tw up`
on the same state directory, used to start the host's audit chain over at
an empty `prevHash`, so `tw audit` said `host:<id> BROKEN at N (prevHash
does not link to the previous record)` and exited non-zero although
nothing was lost or changed.

A host that shuts down cleanly with every audit record delivered now saves
the hash of its last record (`audit-head` in its data directory), and on
the next start continues its chain from it, if mgmt's stored head is that
same hash. `PUT /v1/hosts/{id}` answers the head in a new `auditHead`
field. In every other case the host starts a new segment as before, and
`tw audit` still reports it: after a crash or an unclean exit, when records
were still queued at shutdown, or when the chain at mgmt no longer ends
with the host's last record.

A chain that already shows a break from an earlier restart keeps showing
it; only restarts after the upgrade (of both mgmt and the host) continue
the chain.
