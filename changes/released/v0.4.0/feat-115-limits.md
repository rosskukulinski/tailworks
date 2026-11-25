---
kind: feature
upgrade: [protocol, config-default]
---
Process-tier workloads now have a V8 heap cap: each isolate in a workerd
(the Worker and each Durable Object class) is capped at 1024 MiB
(`tw-hostd -workerd-heap-mib`), and a Node-tier app runs with
`--max-old-space-size=2048` (`tw-hostd -node-heap-mib`) unless its
`node.args` or `NODE_OPTIONS` set their own. Past the cap the workload's
process aborts and hostd restarts it, so one runaway allocation can't take
the host's memory. `-1` turns either cap off.

The mgmt API rate-limits its tailnet callers: each WhoIs node may make 100
requests/s (bursts of 200), each host 1000/s (bursts of 2000), and non-host
callers together hold at most 256 requests at once. Over a limit is the new
error code `429 rate_limited` with `Retry-After`. mgmt's own node (the
operator console) and the local socket are not limited. Configure with
`tw-mgmt -api-rps`, `-api-host-rps` and `-api-inflight` (`-1` = off).

`docs/security-model.md` now states what availability isolation each tier
gives (the process tier gives none beyond the heap cap), and
`docs/limits.md` gives the reason for every limit tailworks deliberately
doesn't set.

Operator action: none, unless a workload legitimately needs a V8 heap over
1 GiB (raise `-workerd-heap-mib`) or a script drives the API faster than
100 requests/s from one node (raise `-api-rps`).
