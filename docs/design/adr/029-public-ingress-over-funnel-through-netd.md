# ADR-029: Public ingress over Funnel, through netd on a stable per-workload public node; tsidp deferred

**Date:** 2026-11-13
**Status:** Accepted (2026-11-16, built and shipped in v0.2.0: `tw expose`,
[docs/public-ingress.md](../../public-ingress.md)). Proposed 2026-11-13 after a
design interview; where the build differs, see "Implementation notes" at the end.
tsidp (D4) stays deferred.

**Context.**
- Cloudflare Workers are public by default. Many apps need a public
  surface: webhook receivers, public pages, OAuth callbacks, and MCP
  servers used by hosted clients.
- tailworks today is tailnet-only:
  - every workload node and object node runs netd on plain `:80`;
  - netd does `WhoIs`, strips identity headers, stamps `x-tw-cf` and
    reverse-proxies to workerd (tailworks `netd/ingress.go`);
  - mgmt already serves its WIF issuer over tsnet `ListenFunnel :443`.
- Measured: the first public fetch for a new node name took 76–107 s
  (certificate issuance).
- Verified on 2026-11-13 (tailscale.com KB 1223 and 1312;
  `tailscale.com@v1.105.0-pre` `ipn/serve.go` and
  `ipn/ipnlocal/serve.go`; tsidp `8d2d5b9`):
  - **Funnel:** ports 443, 8443 and 10000 only, TLS only, terminated on
    the node. DNS names must be `*.ts.net`. Public DNS takes up to
    10 min. Let's Encrypt lockouts can last up to about 34 h.
  - **Funnel and identity:** Funnel traffic carries no identity headers.
    A tsnet Funnel connection is an `ipn.FunnelConn` whose `Src` is the
    real client address. tailscaled's Serve adds
    `Tailscale-Funnel-Request: ?1` and `X-Forwarded-*`; tsnet adds no
    headers.
  - **tsidp:** experimental. `/authorize` **refuses Funnel requests and
    tagged nodes**, so it signs in only untagged tailnet users on a
    tailnet device.
- **Not verified:** Funnel's bandwidth and request-rate figures (the docs
  say only "non-configurable bandwidth limits"), and whether Funnel works
  on Tailscale Services.
- The first `getByName` for a new name mints a tailnet node, so an
  unguarded public `/agents/<class>/<name>` would be a node-minting
  oracle.

**Decision.**
1. **Opt-in (D1).**
   - The tenant declares path prefixes in `tailworks.jsonc`:
     `"public": {"routes": ["/webhooks/*", "/oauth/callback"]}`.
   - There is no `"public": true` shorthand; `"/*"` must be written out.
   - Routes take effect only if the operator has set
     `publicIngress: allow` for the tenant. `tw up` sets it
     automatically in single-tenant mode.
   - `tw expose off|on <workload>` is an operator override. It survives
     redeploys and closes the listener in under a second (a netd kill of
     the public node).
   - Paths not on the list get a 404 from netd and never reach workerd.
2. **Topology (D2).** No new gateway component.
   - Each public workload gets **one extra tsnet node, named
     `<workload>`**, with no host suffix.
     - It's run by the host that holds the workload's lease, and its
       state and certificate cache move through custody like object node
       state. The URL therefore survives host moves.
     - On it, netd listens with `ListenFunnel :443` and `FunnelOnly()`,
       and delivers in-process to the workload's workerd.
   - Object nodes and `<workload>-<host>` nodes are never public. DOs are
     reached through the workload's routes (`routeAgentRequest`).
   - Rejected alternatives:
     - Funnel on `<workload>-<host>`: unstable URLs.
     - A dedicated ingress gateway: a new hop and single point of
       failure, and itself a tailnet caller to every workload.
     - A bring-your-own front door: it looks like a tailnet caller. It is
       deferred, possibly as a `github.com/rosskukulinski/tailworks/cap/front-door` cap.
3. **Requester identity (D3).**
   - `getCaller()` stays tailnet-only and throws
     `TailnetError("public_request")` on public traffic, so existing
     handlers fail closed.
   - The new `getRequester()` returns
     `{kind:"tailnet", …Caller} | {kind:"public", ip, proto:"https", host, traceId}`.
   - `kind` comes from netd's listener (`FunnelConn`), never from
     headers.
   - On public requests only, netd strips every identity header, as it
     does today, then sets `CF-Connecting-IP` and a single-valued
     `X-Forwarded-For` from `FunnelConn.Src`, plus
     `X-Forwarded-Proto: https`.
   - No geo: `request.cf.country` is absent in v1.
4. **tsidp (D4): deferred.**
   - v1 public ingress is anonymous only.
   - A later ADR starts from the verified constraint (no `/authorize`
     over Funnel, no tagged nodes). Its candidate design is netd as an
     RFC 9728 resource server that verifies tsidp bearer JWTs for hosted
     MCP clients.
   - mgmt's WIF issuer stays separate from any user IdP.
5. **Limits and spawn amplification (D5).**
   - **netd limits on the public listener:**
     - defaults: 50 req/s per workload (burst 100), 10 req/s per client
       IP, a 1 MiB body, 32 KiB of headers, a 30 s timeout and 256
       concurrent connections;
     - WebSocket and SSE upgrades are refused unless the route sets
       `"upgrade": true`;
     - errors are `429` with `Retry-After`, and `413`;
     - each value can be overridden in `public.limits`, up to an
       operator ceiling.
   - **Public context can't create objects.**
     - The platform wrapper runs public requests with
       `principal = public`.
     - In that frame, `get`/`getByName` on a name hostd hasn't
       registered throws `public_create_denied` *before* the instance or
       its storage exists, and `ctx.tailnet.spawn()` is always refused.
     - Existing objects are reachable.
   - **Opt-in creation:** a class may set
     `"public": {"create": true, "maxObjects": N}`.
     - N is required, at most 1,000, and counts toward the tenant quota.
     - Creations use a separate throttle lane at 10% of the registration throttle rate
       (20/min), and objects are recorded with `origin: public`.
     - `"node": false` classes follow the same rules.
6. **Policy and preflight (D6).**
   - **Tag:** public nodes carry **`tag:tw-public`**, owned by
     `tag:tw`, with no grants.
   - **Snippet** `tw deploy` prints when the policy is missing it:
     ```jsonc
     "tagOwners": { "tag:tw-public": ["autogroup:admin", "tag:tw"] },
     "nodeAttrs": [ { "target": ["tag:tw-public"], "attr": ["funnel"] } ]
     ```
   - `internal/policy.Check` **narrows** `funnel` to `tag:tw-mgmt` and
     `tag:tw-public` (today it allows any `tag:tw*`).
   - **OAuth:** no new scopes; `auth_keys` + `tag:tw` covers minting
     the new tag, and `policy_file:read` stays optional. OAuth mode stays
     Funnel-free until a workload declares routes.
   - **Deploy preflight order:**
     1. the tenant gate (`public_ingress_not_allowed`);
     2. the tagOwner and nodeAttr, if the policy is readable;
     3. the public node's netmap HTTPS and Funnel flags (`https_required`
        / `funnel_required`, the same fixes as `tw up`);
     4. a name check: `public_name_taken` if MagicDNS would de-duplicate
        `<workload>`, with `tw-mgmt` and `tw-host-*` reserved;
     5. a certificate prefetch.
   - Deploy prints the URL with "first certificate ~1–2 min, public DNS
     up to 10 min", and `--wait-public` blocks until an external probe
     succeeds.
   - **`tw doctor` rows:** `https_certs`, `public_ingress`,
     `funnel_attr`, `public_cert`, `public_probe`.
7. **Audit (D7).**
   - **Every public request** gets an audit record in the host chain,
     shaped like tailnet calls, with `requester: {kind:"public", ip}` in
     place of a caller.
   - `public_object_created` and `expose_on`/`expose_off` are recorded
     too.
8. **Out of scope (D8).**
   - Cron Triggers are a hostd timer with no ingress, and get a separate
     ADR.
   - Inbound email can't use Funnel (no port 25). The documented pattern
     is an email provider's inbound webhook to a public route, and
     `email()` stays unsupported.

**Consequences.**
- Public ingress adds one node and one certificate per public workload,
  not per object. The about 90 s first-certificate cost is paid once per
  workload name.
- Public traffic can never be attributed to a tailnet caller, and
  handlers that didn't opt in never see it.
- Per-request audit (D7) means chain growth tracks public traffic: up to
  about 4.3M records per workload per day at the default limit. Storage,
  verification time and retention need sizing before GA.
- The public node's state moving through custody is new. Today only
  object nodes move.
- Apps where each anonymous visitor gets its own DO must opt in, choose a
  cap, and accept 20 creations per minute.

**Open.**
- Whether requests rejected by the limiter (429/413) also get one audit
  record each. Recommendation: aggregate rejections per minute, so a
  refused flood can't grow the chain.
- Funnel's real rate and bandwidth limits, and Funnel on Tailscale
  Services (would retire the extra node). Measure on test tailnet 1 with
  `ttest-*` nodes.
- The tsidp ADR (hosted MCP clients) and the front-door cap.

**Implementation notes (tailworks, 2026-11-13).** Where the build differs from
the decision above:
- **Expose off (D1):** `tw expose off` closes the Funnel listener and resets
  its flows. It does not kill the node: the node and its certificate stay
  warm, so `tw expose on` is fast. Removing `"public"` and redeploying
  deletes the node (a mgmt kill).
- **Node state on a host move (D2):** custody for the public node's state
  is not built. The name is stable because mgmt names the node after the
  workload and the object ID is deterministic. If a host move or another
  device makes MagicDNS de-duplicate the name, hostd refuses to serve it
  (`public_name_taken`), so a renamed URL is never published.
- **Rejected requests (D7, the open item):** per the recommendation,
  requests the limits refused (429/413/431, over `maxConns`) are summarized
  per minute (`public_rejected`). Every other public request gets its own
  `public_request` record, including route refusals and
  `public_create_denied`.
- **Where D5 is enforced:** in the tenant isolate (a guarded `env` and
  `ctx.tailnet` for public fetches), in the platform's Host DO (facet
  objects: `ensure` with `public: true`), and in hostd (`/v1/admit`,
  `ensure` checks).
  - **Residual:** tenant code that reaches the raw bindings (`import { env }
    from "cloudflare:workers"`, or an `env` saved globally) bypasses the
    tenant-side guard. For facet classes the Host DO still refuses a
    `fetch`; plain-DO and `"node": false` classes rely on the guard alone.
  - **Counting:** `maxObjects` and the 20/min lane are counted by the host
    that holds the lease, persisted in `public-creates.json`.
  - **`"node": false` classes:** they are reachable from public requests
    only when they opt into `public.create`.
- **Scope:** `"runtime": "node"` with `"public"` is refused in v1.
- **mgmt is never Funnel-capable (Ross, 2026-11-13).** mgmt's node used to
  serve the WIF issuer over Funnel. The issuer now has its own node,
  `tag:tw-issuer`.
  - **The node:** it has no grants and a Funnel-only listener, serving only
    OIDC discovery and the JWKS.
  - **Policy:** `policy.FunnelTargets` is `tag:tw-public` and
    `tag:tw-issuer`, and `Check` refuses `funnel` on `tag:tw-mgmt`.
  - **Cost:** `tw up --wif` needs a second join for the issuer node (a
    login, or `--issuer-authkey-file`). The issuer URL also changes, so
    existing trust credentials must be re-created.
  - **RFC §4.4:** production issuers should still be at an operator-owned
    origin. This only changes where the self-hosted Funnel issuer runs.
