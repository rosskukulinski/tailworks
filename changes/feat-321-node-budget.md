---
kind: feature
upgrade: [cli-json, config-default]
---
tailworks now says how many tailnet nodes an app uses. `tw policy` and
`tw deploy`'s preflight print an estimate line (for example "1 workload
node + 1 node per Room object") with the tenant's live node count;
`tw ls` has a `NODES` line and `tw doctor` a `node_budget` check. All of
them warn when the tenant's nodes are within 80% of its `maxObjects` or of
its hosts' node capacity (`tw-hostd -capacity-nodes`). With `--json`,
`tw policy`, `tw deploy` and `tw ls` carry the same numbers in a new
`nodes` field.

Every node is a device on your tailnet, so your Tailscale plan's device
limit applies. docs/limits.md has a new "Tailnet node budget" section with
the counting rule, and docs/recipes.md says when to give a Durable Object
class its own node and when to set `"node": false`.
