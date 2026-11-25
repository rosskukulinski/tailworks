# ADR-006: Every object gets its own tailnet identity

**Date:** 2026-11-07
**Status:** Accepted (Ross). Amended 2026-11-21 with the alternatives and the measurements (below).
**Decision:** Each DO-like object instance, not just each deployment, is a distinct node in its owner's tailnet, with its own MagicDNS name, its own tags and grants, and its own entries in flow logs.
**Consequences:**
- Node count scales with the object count, so churn, netmap size, ephemeral cleanup and per-node host memory become first-order design problems.
- Hibernation must keep the identity reachable: the host-side node stays up while compute sleeps.
- The ADR-005 open question on workload granularity is resolved: "per object".

## Amendment 2026-11-21: alternatives, and the measurements (issue #114)

The decision was recorded without the alternatives. These were on the
table, and still are for any one class:

- **One node per workload, with attribution in the application.** The
  workload's node is the only tailnet identity; tailworks names the object
  in a header, a log field or an on-behalf-of assertion (ADR-010). This is
  what `"node": false` and plain-DO egress (`"egress": "workload"`) ship
  today, and it costs one node per workload instead of one per object.
  It lost as the default because the tailnet can't see the object: grants
  can't tell two objects of a workload apart, flow logs and WhoIs show the
  workload, and a kill is all or nothing for the workload. The attribution
  is something tailworks says, which a peer has to trust, rather than
  something the coordination server enforces.
- **Tailscale Services.** One `svc:` name in front of the workload's
  nodes, so callers address a stable name and the hosts behind it can
  change. Services give a workload a name and HTTPS (tailworks uses them so:
  [services.md](../../services.md)), but a Service is a destination, not
  a source: outbound traffic still leaves as whatever node sent it, and a
  Service per object would cost a Service per object, with nothing gained
  over a node. Services are also not available on Headscale (ADR-015).
- **Per-class opt-in.** A class gets its own nodes only when it asks for
  them. This is what was built: a class gets nodes when it lists tags in
  `tailnet.objects`, and `"node": false` makes it internal
  ([concepts.md](../../concepts.md)). It refines the decision rather than
  replacing it: the per-object identity is there for the classes whose
  objects act on their own (agents, sub-agents, anything that calls other
  tailnet services), and classes that are only state pay nothing.

  *Amendment (#492):* an unlisted class is internal (`"node": false`: a
  plain DO, as on Cloudflare), whether or not the app has a
  `tailworks.jsonc`. Listing it with tags opts it into a node per object
  and facet mode. Before, any `tailworks.jsonc` turned every unlisted
  class into a facet with no node, so adding a three-line file changed DO
  semantics and storage.

Per-object won for the classes that opt in because it is the only option
where the tailnet itself (grants, WhoIs, flow logs, kill by device) sees
each object, with no tailworks code in the trust path.

**What it costs, measured** ([limits.md](../../limits.md#per-node-memory-measured)):
about 2.5 MB of host RAM per idle node and 4 MB under request load at
N = 100–500 on Linux and macOS, when objects see only their callers. When
every object sees every other, the per-node cost grows with N (4.7–12 MB at
N = 100), and 500 such nodes in one process did not finish on Linux. One
host registers 50 fresh nodes with production control in 2.6–6.3 s, at the
rate netd's join concurrency allows
([limits.md](../../limits.md#registration-rate-measured)). The host's
advertised node capacity is derived from these figures (`hostd/capacitynodes.go`).
