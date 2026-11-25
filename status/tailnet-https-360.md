Last update: 2026-11-24 17:00 PT. Maintainer decisions applied: caller grant on tcp:443 for https apps, public request.url https://, ADR draft.

# Tailnet HTTPS for workloads (issue #360)

## What's built

- `tailworks.jsonc` `"tailnet": {"https": true}` (schema, `hostd/bundle`,
  both runtimes). Host feature `tailnet-https`, so placement skips older
  hosts.
- netd (`netd/tailnethttps.go`): `ObjectOptions.HTTPS` and
  `Registry.SetTailnetHTTPS(id, on)` open a `:443` listener on a ready node
  (tsnet `Listen`, never Funnel), TLS from `lc.GetCertificate`, HTTP/1.1
  only, flows tracked (Kill RSTs them), cert prefetch as for public nodes.
  The handler is the same `serveIngress` as `:80`; requests that arrived
  over TLS get `X-Forwarded-Proto: https` after every client copy is
  stripped. Refused with `tailnet_https_unavailable` (412) when the node's
  status has no `CertDomains` or MagicDNS is off. `Config.HTTPSCert` is the
  test seam.
- hostd (`hostd/tailnethttps.go`): the deploy waits for the workload node
  and turns the listener on (or off) before the new instance is started; a
  refusal fails the deployment with the code and leaves the active one
  serving. A remint of the workload node comes back with the listener
  (`startNodeLocked` → `opts.HTTPS`). `ingressHandler` sets
  `cf.tw.https` for workload-node tailnet requests that came over TLS
  and for every public request (never object nodes); platform.js rewrites
  `request.url` to `https://` for those. Node tier: `X-Forwarded-Proto` is
  now forwarded to the app.
- `tw deploy` prints the `https://` URL (human and `--json` ready event);
  `tw dev` ignores the setting (`hostd.Config.TailnetHTTPSOff`) and prints
  a note.
- e2e/compat: `Project.TailnetHTTPS`; `ag-mcp-worker-auth` sets it, the
  deployed run calls `https://<magicDNS>` (dialled at the IP) and sets
  `MCP_RESOURCE` to that origin. Its expectation is now pass; the stale
  deployed XFAIL record was dropped from `upstream-results.json`, so
  COMPAT.md shows "not run" for the deployed column until the next
  `make e2e-compat`.

## Decisions

- `:80` stays served, not redirected (platform callers, service bindings,
  POSTs).
- Only the workload node. Per-object nodes, Service VIPs: left out.
- Maintainer, 2026-11-24: public routes' `request.url` is now `https://`
  too (`markHTTPSRoute` marks every public request; behaviour change noted
  in the changes fragment, public-ingress.md and COMPAT.md;
  `hostd:TestPublicIngress` asserts it).
- Maintainer, 2026-11-24: the caller grant covers `tcp:443` too, only for
  apps with `tailnet.https` (`mgmt.CallerPorts`, used by the printed grant
  in `tw deploy`/`tw policy` and the one `--apply-policy` writes).
- Design note: `docs/design/adr/tailnet-https.md` (ADR-tailnet-https,
  Proposed). Its CI docs test needs #409 (un-numbered drafts).

## Tests run

See the PR body (Tests section).

## Gaps / not verified against a real tailnet

- A real ts.net certificate on a workload node (issuance time, LocalAPI
  `GetCertificate` under netd's `OmitAuth` local client), real callers on
  :443, and the deployed `ag-mcp-worker-auth` run (`make e2e-compat`,
  needs tailnet credentials).
- Headscale: not run; Headscale is expected to report no CertDomains.
- microVM tier: the platform.js rewrite is shared, but not run here.
