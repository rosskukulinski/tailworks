---
kind: fix
upgrade: []
---
The lock template's client (`templates/lock/src/client.ts`): `withLock` now
throws `lease_expired` when the lease's deadline passed before your function
returned, even if its expiry timer hadn't fired yet (a busy event loop). It
used to return normally, though the work may have outlived the lease. Apps
that copied `client.ts` from an earlier template can copy it again.
