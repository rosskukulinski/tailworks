---
kind: feature
upgrade: [feature-string]
---
A host can send every workload's internet egress through a tailnet exit
node: `tw up --worker-internet=exit-node=<node>` (or `tw-hostd
-worker-internet=exit-node=<node> -container-internet=exit-node`). The
host's own node (`tag:tw-host`) uses the exit node, so the destination
sees the exit node's address and the host's resolver never sees the names
(they resolve at the exit node, or at `dns=<ip>` through it). netd's SSRF
guard is unchanged. When the exit node is missing, not offered, offline or
refuses DNS, internet egress is refused with the new `exit_node_unavailable`
code; the host never falls back to its own network. `tw status` and
`tw doctor` show the exit node and why it is unusable.

A workload can require it with `tailnet.egress.via: "exit-node"` in
tailworks.jsonc. Such a deploy needs the new host feature
`internet-exit-node`, which a host advertises only when its path is an
exit node; mgmt places it only there (`feature_unavailable` otherwise).

Operator action: none unless you use it. To use it, grant `tag:tw-host` →
`autogroup:internet` in the tailnet policy and approve the exit node's
routes.
