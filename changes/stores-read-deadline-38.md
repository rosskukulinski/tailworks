---
kind: fix
upgrade: []
---
KV, D1 and R2 reads no longer stall when mgmt is slow or unreachable
(#38). A read that wants fresher data than the host has waits at most 2 s
for mgmt, then answers from the host's copy; while mgmt stays unreachable,
later reads answer at once and the host retries in the background every
5 s. Concurrent catch-ups with mgmt share one request, and hostd gives up
on a journal read after 10 s instead of 30 s. Before, a slow or
black-holed mgmt could hold each D1 read for up to 30 s per queued
catch-up.

Writes are unchanged: they still fail while mgmt is unreachable (an
acknowledged write is always in mgmt's journal). The docs now say what
happens to reads during a mgmt outage: a workload with a Durable Object
class (not `"fence": "never"`) is stopped by its host after 30 s without
mgmt, stores included; only workloads without one keep serving reads.
