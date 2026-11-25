---
kind: security
upgrade: [protocol, config-default]
---
One client can no longer fill a workload's public (Funnel) connections and lock everyone else out. The public listener now caps connections per client address (`public.limits.maxConnsPerIp`, default 32; an IPv6 client counts per /64), gives a new connection 5 seconds to send its first request, and closes upgraded (WebSocket) connections after `public.limits.upgradeLifetimeSeconds` (default 3600). Refused connections are counted in the per-minute `public_rejected` audit summary (`ipConnsRefused`) and in the new `tw.public.rejected` metric (#69).

Operator action: WebSocket clients of a public workload must reconnect when the lifetime ends; raise `upgradeLifetimeSeconds` or `maxConnsPerIp` in `tailworks.jsonc` if your users need longer sessions or share one address. A host on this version keeps the new defaults even while mgmt is older.
