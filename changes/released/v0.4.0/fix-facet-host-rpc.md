---
kind: security
upgrade: []
---
A facet object's platform host no longer answers tenant code on its
internal methods. The Worker's raw binding (`import { env } from
"cloudflare:workers"`) and a facet's bindings both reach that host, and
workerd exposes a Durable Object's methods over RPC. Its helpers are no
longer methods, and the calls only the platform makes (kill, container
intercept, alarm delegation) now need a key only the platform holds, so
tenant code can no longer start an object hostd did not admit, set its
alarm, or kill it without `tw kill`.
