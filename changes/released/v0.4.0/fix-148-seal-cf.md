---
kind: security
upgrade: []
---
A caller in `request.cf` is now sealed. hostd seals the caller of every
tailnet request with an HMAC under the instance's key, and the platform
checks the seal before a Durable Object, the Worker or an audit
`actingFor` uses it. Tenant code that forwards a request in-process with a
caller it made up (a Durable Object stub `fetch`, or a `Request` passed by
RPC to a facet object) now delivers no caller, so `getCaller()` throws
`no_identity`. Forwarding the request you received still carries its
caller. A seal lasts five minutes and can be replayed only for the same
real caller, inside the same workload (RT3-08 / H08, #148).
