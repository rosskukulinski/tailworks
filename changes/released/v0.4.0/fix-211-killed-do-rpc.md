---
kind: security
upgrade: []
---
A killed plain Durable Object (`"egress": "workload"`) no longer runs tenant
code through its RPC methods. Before, only its `fetch` checked the kill, so
a stub kept from before `tw kill`, or the raw binding from
`import { env } from "cloudflare:workers"`, could still call its methods.
Now every method or getter reached from outside the object fails with
`[killed]` (the caller's request gets `410 killed`), and its `alarm` and
WebSocket handlers do nothing. A facet object's platform host refuses to
start a killed object's code by any path.
