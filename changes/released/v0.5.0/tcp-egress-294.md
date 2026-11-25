---
kind: feature
upgrade: []
---
Workers and Durable Objects can open raw TCP sockets. Stock `connect()`
from `cloudflare:sockets` now works, so database and cache drivers built for
Workers (postgres.js, and others that use `connect()`) reach a server on your
tailnet. A socket gets the identity and policy of `fetch()`:

- From a facet-mode Durable Object it leaves as the object's own node.
- From the Worker and from plain-DO objects it leaves as the workload node.

The same refusals apply (`forbidden_destination`, `peer_not_visible`,
`internet_egress_unavailable` under `tailnet.egress.internet`). `tw kill`
resets an object's open sockets. Each flow is a trace span and an `egress`
audit record (bytes and duration), and each refusal is a `deny` record.

`ctx.tailnet.connect()` is built. It returns the same socket, and a refusal
rejects `opened` with a typed `TailnetError`. `{ tls: "netd" }` has the host
do TLS to the destination. In a plain-DO object it throws `not_supported`.
`tailnet.connects` entries can name a port (`"tag:db:5432"`), so `tw deploy`
checks and prints the grant for that port. Recipe:
docs/recipes.md#connect-to-postgres-on-your-tailnet. Design: ADR-052.

Not yet: `secureTransport: "on"` / `"starttls"` on stock `connect()`
(workerd can't hand a TLS socket to tailworks' egress). On the tailnet, turn
the driver's TLS off: WireGuard already encrypts the flow. Each object has at
most 256 open sockets (`socket_limit`).

Operator action: upgrade hosts before deploying apps that use `connect()`.
On an older host `connect()` still fails.
