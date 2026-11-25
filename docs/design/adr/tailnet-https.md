# ADR-tailnet-https (Proposed): A workload can also be served on https:// on the tailnet, and request.url says how it was reached

**Date:** 2026-11-24
**Status:** Proposed (branch `tailnet-https-360`, issue
[#360](https://github.com/rosskukulinski/tailworks/issues/360)). Gets a
number when it merges (AGENTS.md rule 5).

## Context

- A workload node serves its attributed ingress on `:80` only, so a Worker
  always sees `http://<name>.<tailnet>.ts.net/…` as `request.url`. On
  Cloudflare it would see `https://`.
- Some code refuses a non-HTTPS origin outright. The case that prompted
  this: `@cloudflare/workers-oauth-provider` requires an `https://` resource
  URL, so `mcp-worker-authenticated` from cloudflare/agents couldn't start
  when deployed (e2e/compat `ag-mcp-worker-auth`).
- A tsnet node can serve TLS with its own ts.net certificate when the
  tailnet has HTTPS certificates on (the LocalAPI's `GetCertificate`). mgmt's
  node already does this for `tw link`.
- Public routes come over Funnel, which terminates TLS, but the Worker was
  still handed an `http://` URL (with `X-Forwarded-Proto: https`).

## Decision

1. **Opt in per workload:** `tailworks.jsonc` `"tailnet": {"https": true}`.
   netd opens a `:443` listener on the workload node (tsnet `Listen`,
   tailnet only, never Funnel), with TLS from the node's own certificate
   and HTTP/1.1 only, so upgrades still hijack. Its handler is the same
   attributed ingress as `:80` (WhoIs, certificate narrowing, header
   stripping). Its flows are tracked, so a kill RSTs them. The certificate
   is requested as soon as the listener opens.
2. **`request.url` follows the transport.** netd sets
   `X-Forwarded-Proto: https` only on requests that came over TLS, after
   stripping every client copy. hostd turns that, and only that, into the
   route marker `cf.tw.https`, which no client can set, and the
   platform worker rewrites `request.url` to `https://` before the tenant's
   `fetch()`. We use our own marker instead of workerd's
   `forwardedProtoHeader`, so one code path covers every workerd tier and
   no header decides the scheme. A Node-tier app gets the header.
3. **Public routes get the same treatment** (`cf.tw.https` on every public
   request), so their `request.url` is `https://` too. This changes
   behaviour for existing public apps; the release note calls it out.
4. **`:80` stays, unchanged and not redirected.** `ctx.tailnet.fetch`,
   service bindings, same-host hops and Service VIPs all use it, and a
   redirect would turn POSTs into GETs.
5. **No certificates means no deploy.** The deploy waits for the workload
   node and opens the listener before the new instance starts. If the
   node's status shows no `CertDomains`, or MagicDNS is off, the deploy fails
   with `tailnet_https_unavailable` and the active deployment keeps
   serving. Advertising an `https://` URL that never answers would be worse.
6. **Placement and grants:** host feature `tailnet-https`. For such an app
   the caller grant (printed, and written by `--apply-policy`) covers
   `tcp:443` as well as `tcp:80`; other apps keep `tcp:80` only.

## Left out

- Per-object (facet) nodes and Tailscale Service VIPs stay `http://`.
  #360 doesn't need them, and a Service would need a certificate for its
  `svc:` name.
- No HTTP→HTTPS redirect option.
- `tw dev`'s local control issues no certificates, so it serves `http://`
  only and prints a note.
- Headscale issues no ts.net certificates, so the deploy fails there with
  `tailnet_https_unavailable`.

## Consequences

- Code that needs an HTTPS origin runs unmodified once one setting is on.
- A Worker can trust `request.url`'s scheme: it is `https://` exactly when
  the request was encrypted end to end to the node (or by Funnel).
- The first `https://` call after a new name's first deploy can wait
  30 to 100 s for certificate issuance.
