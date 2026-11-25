---
kind: fix
upgrade: []
---
A host that was stopped (a paused VM, a frozen process) for longer than
the 45 s lease no longer acts for a workload another host has taken over.
hostd fenced stateful workloads only after a heartbeat *failed*, and a
stopped host never saw one fail: its first heartbeat on resume succeeded
and carried the revoke, and until that was handled its Durable Objects
kept making egress calls (seen 2.7 s and 4.8 s after the new copy's
first, #501). A workload with Durable Objects now refuses ingress and
egress (`upstream_unavailable`) whenever the last successful heartbeat was
sent more than `FenceAfter` (30 s) ago, and serves again only after a new
heartbeat succeeds, with its revoke applied first.
