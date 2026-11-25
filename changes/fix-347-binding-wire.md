---
kind: security
upgrade: []
---
A Worker's or Durable Object's `fetch()` can no longer forge a service
binding call (red-team H-binding-wire, #347). `Tailworks-Binding` is the
header a call to a named entrypoint or an RPC method carries between
hosts. Before, tenant code could set it on a plain `fetch()` to another
workload of its tenant and run that workload's named entrypoints and RPC
methods without declaring a `services` binding. The platform and hostd
now remove the header from tenant requests. The callers are still limited
to tailworks nodes of the same tenant that the tailnet policy lets reach
the target.

One path remains, and the docs now say so: code that opens a raw socket to
the target (`connect()`, or a Node app) can write the header itself. Within
a tenant, the grant is the authorization, not the binding declaration. An
entrypoint that must not be open to every workload of the tenant should
check `getCaller()` (docs/wrangler.md, service bindings).
