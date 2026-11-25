---
kind: fix
upgrade: [config-default]
---
`tw up --warm-keys`, `--pool-nodes` and `--port-mapping` now take effect in
the default single-process layout. Before, they were saved to `tw.json` but
only `--separate-host` read them, so a saved `--warm-keys 0`,
`--pool-nodes 0` or `--port-mapping` was silently ignored. With port
mapping off (the default), mgmt's own node now also skips the port-mapping
probe (#457).

`tw up --heartbeat`, `--kill-tombstone`, `--device-delete-delay` and
`--push-port` are deprecated. They still parse but are ignored (tw up says
so), and will be removed in a later release. The host uses a 1s heartbeat,
a 12s kill tombstone and an 8s device delete delay; values an older tw
saved in `tw.json` are still used. `--push-port` never worked with a
non-default port: the tailnet policy grants only port 7474 (#457).

`tw up` now checks that workerd runs (`workerd --version`) before it mints
mgmt's node key, and fails with `workerd_broken` and `tw doctor`'s fix
when it does not. On Linux without Landlock it prints the
"NO PROCESS SANDBOX" warning before it starts (#480).

Operator action: if you saved `--warm-keys 0`, `--pool-nodes 0` or
`--port-mapping` in the default layout, they now apply on the next start.
