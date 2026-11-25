---
kind: feature
upgrade: [protocol, feature-string]
---
wrangler `services` (service bindings) work between workloads of one
tenant: `env.AUTH.fetch()` (streaming bodies, WebSocket upgrades) and JS RPC
to the target's default `WorkerEntrypoint` or a named `entrypoint`, with
structured-clone arguments and results. The call is a tailnet call from the
calling node (the workload, or the facet object), so the target's
`getCaller()` names it (`getCaller(this.ctx)` in an RPC method), and it
needs a grant from the caller's tags to the target's like any tailnet call.
`tw types` emits each binding as `Fetcher`, and `tw deploy` no longer warns
that `env.<binding>` will be undefined. Functions, `RpcTarget` stubs and
streams can't be RPC values (`rpc_not_forwardable`); a missing target is
`service_not_found`.

A tailnet call between objects or workloads on the **same host** now works
without `tw up --allow-sibling-egress`: netd delivers it in process to the
destination's ingress, after the same netmap and packet-filter checks a call
from another host gets (so `addMcpServer` to an MCP server on the same host,
and clients of the `lock` workload on its host, work). `--allow-sibling-egress`
now only makes those calls hairpin over WireGuard.

Operator action: mgmt gains `GET /v1/tenants/{id}/workloads/{workload}/service-bindings/{binding}`
(host role). A deployment with `services` is placed only on hosts that
advertise the `service-bindings` feature, so upgrade mgmt and hosts first.
