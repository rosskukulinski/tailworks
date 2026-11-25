---
kind: breaking
upgrade: [feature-string, config-default]
---
Workers now have internet egress, on by default, as on Cloudflare
(ADR-049, #161). A Worker's, Durable Object's or Node
app's `fetch("https://api.example.com/...")` now works instead of failing
with `internet_egress_unavailable`. The request leaves from the host's own
network, not from a tailnet node, so the API sees the host's address (each
host has its own) and no tailnet identity. Tailnet egress is unchanged.

An SSRF guard always applies: loopback, private (RFC 1918, ULA),
link-local (including cloud metadata at `169.254.169.254`), CGNAT and
tailnet ranges and the host's own addresses are refused with
`forbidden_destination`. The check is on the address actually dialled,
so a DNS name that resolves to any such address is refused too. Names
under `.internal`, `.local`, `.localhost`, `.lan` and other special-use
suffixes are never looked up. The first internet flow per object and
destination is an `egress` audit record.

`tailnet.egress.internet` in tailworks.jsonc is now one setting for
Workers and containers: `false` (or `[]`) turns internet egress off, a
list of hosts limits it, and `true` also lets containers started with
`enableInternet` reach any public host. When it is absent, Workers may
reach any public host and containers none, as before.
`internet_egress_unavailable` now means internet egress is turned off by
config. `tw-hostd` has a new `-worker-internet` flag (`direct`, the
default, or `off`), and hosts advertise the `worker-internet` feature,
which a bundle that uses `true` or `false` needs.

Operator action: workloads without `tailnet.egress.internet` gain internet
egress as soon as their host runs this release, without a redeploy. To
keep a workload off the internet, set `"egress": {"internet": false}` (or
`[]` while some hosts are still on an older release) and redeploy it
before upgrading, or run `tw-hostd -worker-internet=off`. Workloads with an
allowlist keep it for containers, and their Workers may now reach the
listed hosts.
