# ADR-053: Service bindings are tailnet calls from the calling node, resolved by mgmt; same-host calls take netd's in-process hop

**Date:** 2026-11-23
**Status:** Accepted (2026-11-23; issue #293, built on branch
`service-bindings-293`). Drafted as `ADR-service-bindings`. The cross-host
channel is to be revisited together with ADR-do-split (PR #11).

**Context.**

- wrangler `services` was not read: `tw deploy` warned that `env.AUTH`
  would be undefined, and a multi-Worker project had to rewrite its calls
  as `fetch()`es to MagicDNS names.
- Each workload is its own workerd process (its own VM on the microVM
  tier), with its own instance key and DO storage, and is placed on its
  own (ADR-012, `mgmt/placement.go`). A binding's target is in another
  process, often on another host, so it can't be an in-process workerd
  `service` binding.
- netd refused a tailnet call to an object on the same host as a "sibling
  hairpin" (`forbidden_destination`) unless `--allow-sibling-egress` was
  set. RFC §4.8 says a same-host destination is "delivered through netd's
  internal hop, which sets the cf blob from the attributed source"; that
  hop wasn't built. So `addMcpServer` to an MCP server on the same host
  failed, as did a client of the `lock` workload on its host.
- ADR-019 rule 2 says co-located objects get no peer visibility from
  generated grants: netd's memory grows with the square of the peers.
- A tailnet request already carries what a binding call needs on the
  callee: WhoIs on the transport peer, the caller's node certificate
  (ADR-010) and the seal hostd puts on the caller (#148).

**Decision.**

1. **The same-host hop (netd, `netd/samehost.go`).** A flow from an object
   to another object of the same registry is delivered in process to the
   destination's own attributed ingress, on a connection whose remote
   address is the source's tailnet address. It is checked exactly as a
   WireGuard flow would be:
   - the destination must be a peer in the **source's** netmap (the name or
     IP resolves there as for any tailnet call), so no peer visibility is
     added and ADR-019 rule 2 holds;
   - the **destination's** packet filter must accept the flow (source
     address → destination address and port, TCP SYN). That's the grant
     check WireGuard would make. Like WireGuard's filter, it applies to new
     flows, not to a pooled connection already open;
   - the destination's ingress runs WhoIs with its own LocalClient, applies
     the caller's certificate and the kill list, and stamps the cf blob.
   A Service VIP whose only reachable hosts are on this registry is
   delivered to that object's Service ingress the same way (WhoIs scoped
   to the VIP, H22). Both ends are tracked flows, so a kill of either side
   resets the hop. Only the ingress port is served; another port is
   refused like a closed one (`dial_failed`). `--allow-sibling-egress`
   remains, now meaning "dial over WireGuard instead" (tests).
2. **Binding wiring.** `hostd/bundle` parses `services` (`binding`,
   `service`, optional `entrypoint`; `internal/svcbind`, shared with mgmt).
   `environment` and `props` are refused at deploy. `env.<binding>` is a
   workerd service binding to the platform's `ServiceBinding` entrypoint
   with `{binding}` props; a facet's env gets the same entrypoint with its
   class and name, as for stores and queues. `tw types` emits `Fetcher`.
   A deployment with `services` needs the `service-bindings` host feature.
3. **Target resolution.** The target is the workload named by `service` in
   the **same tenant**: there is no way to name another tenant's workload.
   hostd asks mgmt (`GET /v1/tenants/{id}/workloads/{workload}/service-bindings/{binding}?epoch=`,
   host role, fenced on the caller's lease) and mgmt checks that the
   caller's latest deployment declares the binding, then answers with the
   target's entry node: its MagicDNS name and status (`404
   service_not_found`, `410 killed`, `503 identity_pending`). hostd caches
   the answer for 5 s per (lease epoch, binding) and drops it when netd
   can't reach the name, so a moved target is found again.
4. **Transport: the tenant's tailnet, as the calling node.** The platform
   sends `fetch()` and RPC calls through the calling node's egress (the
   workload's, or the facet object's, MAC'd) with `x-tw-binding`, which
   only the platform can set (both egress entrypoints strip `x-tw-*` from
   tenant requests). hostd rewrites the request to the target's name and
   netd sends it from the calling node: in process when the target is on
   this host (1), over WireGuard otherwise. So the call needs a grant from
   the caller's tags to the target's, as every tailnet call does
   (`tw policy` and the `tailnet.connects` preflight print it). A
   host-to-host channel that keeps the caller without grants (ADR-do-split
   option (a)) is not built; both ADRs should move to it together if it is.
5. **RPC.** Only structured-clone values cross: the platform encodes the
   arguments (and the callee's platform the result) as tagged JSON
   (`hostd/gen/js/svc.js`: objects, arrays, `undefined`, `NaN`/`±Infinity`/
   `-0`, `BigInt`, `Date`, `RegExp`, `Map`, `Set`, `ArrayBuffer`, typed
   arrays, `DataView`, errors). Functions, `RpcTarget` and RPC stubs,
   streams, `Request`, `Response`, `Headers` and cycles are refused with
   `rpc_not_forwardable`. A thrown error keeps its name and message. The
   call is a `POST` with `Tailworks-Binding: v1; entrypoint=<e>; rpc=<m>`.
   workerd's `capnpConnectHost` is not used: it can't be attributed per
   call. `fetch()` keeps streaming bodies and WebSocket upgrades; a request
   and its response stream one after the other (Go's HTTP/1.1 server
   reads the request first), not full duplex.
6. **Callee.** A call to the default export's `fetch()` is an ordinary
   tailnet request. A call to a named entrypoint or an RPC method carries
   `Tailworks-Binding`. The callee's hostd honours it only from a
   tailworks node of the **same tenant** (a node certificate verified
   against the tenant's key), only on a workload's entry node and never on
   a public request (`binding_forbidden`), and always strips it. On the
   caller's side the header is hostd's to write: the platform's egress
   entrypoints and hostd's HTTP egress handlers remove one that tenant
   code set, so a `fetch()` can't ask for an entrypoint (#347). The
   platform runs the call through `__tw_binding`, an entrypoint hostd
   generates in the tenant main module, which wraps the named entrypoint
   like the default export (guarded env, `ctx.tailnet`, the request's
   create ticket). The default export may be a `WorkerEntrypoint` class
   with RPC methods. Reserved names (`fetch`, `constructor`, the other
   handlers, `__*`) are not callable.
7. **Caller identity.** The callee's `getCaller()` names the calling node:
   the workload's entry node, or the facet object's own node (a plain-DO
   object calls as the workload, as its `fetch()` does). Nothing is
   forwarded from the caller: no seal, no cf, no identity header. The
   callee's hostd seals the caller it got from WhoIs, as for any tailnet
   request. In an RPC method the caller is `getCaller(this.ctx)`
   (`ctx.cf`, with only a caller hostd sealed). Before it sends a named
   or RPC call, hostd waits until mgmt knows the calling node (its ready
   report), so the callee finds the node's certificate.
8. **Authorization.** Within a tenant, the grant (4) plus the tenant check
   (6) are the authorization. The target does not list its callers: as on
   Cloudflare, any Worker of the account can bind any other. The caller's
   `services` declaration is not part of it. The callee sees which node
   called, not which code on that node wrote `Tailworks-Binding`, and code
   that speaks HTTP itself on a raw socket to the target (`connect()`,
   ADR-052; a Node app's tunnel) writes bytes hostd never parses. So a
   named entrypoint or RPC method is not a boundary between workloads of
   one tenant that the policy lets reach each other: one that must not be
   open to all of them checks `getCaller()`. Closing this needs a header
   the calling hostd can sign and the callee can verify (or the
   host-to-host channel of (4)); neither is built (#347). Each call is
   an egress span on the caller and an ingress span on the callee
   (ADR-045); a `traceparent` across the hop is not added yet. netd's
   refusals are audited as `deny` on the caller, as for any egress.

**Consequences.**

- A multi-Worker project ports with its `services` unchanged, plus the
  grants between its workloads' tags.
- Same-host tailnet calls between workloads (MCP servers, the `lock`
  workload, plain `fetch()` to a MagicDNS name) work without
  `--allow-sibling-egress`, under the same checks as calls between hosts.
- A target sees exactly who called. On-behalf-of across the binding
  (`actingFor`, RFC §4.6) stays out of scope.
- An RPC call costs an HTTP round trip and a JSON encoding, and has no
  promise pipelining.

**Not built.** `tw dev` bindings to another workload; `environment` and
`props`; bindings to another tenant (call over the tailnet with grants);
Tail Workers (`tail_consumers`); dispatch namespaces.
