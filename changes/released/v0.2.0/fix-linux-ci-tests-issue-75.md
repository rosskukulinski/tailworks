---
kind: fix
upgrade: []
---
Two netd fixes found while making the test suite pass on Linux.

- **Joins after a restart no longer hang.** A node whose saved key control
  had expired could stay in `NoState`, retrying its login, without ever
  reaching NeedsLogin. Its rejoin then never ended, and it held a join slot
  while it waited. A login error that persists during a rejoin now counts as
  a rejection, and the object is re-minted. Join slots are now handed out in
  the order objects were created.
- **The first call to a just-created object no longer takes up to 5 s.** An
  object that calls one created a moment ago now waits for the new node to
  answer a disco ping, not just for a path to it. Without this, the first
  WireGuard handshake could arrive before the new node knew the caller, and
  was retried only after 5 s.
