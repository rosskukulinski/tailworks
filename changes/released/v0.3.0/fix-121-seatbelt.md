---
kind: security
upgrade: [config-default]
---
macOS: the process-tier Seatbelt profile is now strict. A workerd or Node
app can write only its own dirs, open no network except its own unix
sockets and the TCP ports hostd gives it (a Node app: its egress proxy and
its own port), signal only itself and its children (not tw-hostd, tw-mgmt or
other workloads), and exec only its runtime, its own dirs and the OS dirs.
Node-tier egress on macOS is now reported as `enforced` in `tw status`, and
`tw doctor`'s sandbox check also probes the write and signal denials.

Operator action: none. If the strict profile breaks a workload, start
tw-hostd with `-sandbox seatbelt-legacy` (the previous files-only profile;
kept for one release) and report it.
