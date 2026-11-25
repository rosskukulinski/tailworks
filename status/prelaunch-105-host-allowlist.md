Last update: 2026-11-17 PT. Host allowlist against DNS rebinding (issue #105): built, tests below.

# mgmt Host allowlist (issue #105, red-team H-105)

## The finding

`browserGuard` compared `Origin` only to the request's own `Host`, never
checked `GET`, and mgmt always serves plain HTTP on `:80`. An attacker who
rebinds a DNS name to mgmt's tailnet IP gets a page that is same-origin with
mgmt: it could read `/v1` and post `/ui` forms with the admin's WhoIs
identity.

## What's built

- `mgmt/hostguard.go`: `HostAllowlist` and `MgmtHostNames`. Allowed hosts are
  the node's MagicDNS FQDN and short name, `-tsnet-hostname` and
  `-tsnet-service` (short and under the MagicDNS suffix), and any IP literal.
  Rebinding needs a DNS name, so an IP-literal Host can't be an attacker's
  origin. That covers the CLI (it dials mgmt's IP through the loopback
  proxy), `tw up`'s embedded host (`http://<MgmtIP>`), and the Service VIP.
  Port, case and a trailing dot are ignored.
- `Server.TailnetHandler` checks Host first, for every method and path,
  before WhoIs. A refusal is 421 `misdirected_request`, which names the
  expected hosts. The new code is in `api/openapi.yaml` (regenerated with
  `make api`) and in `docs/errors.md`.
- `browserGuard(hosts, h)`: the Origin must equal Host **and** be on the
  allowlist.
- `internal/mgmtd` sets `api.Hosts` from `SelfInfo` + flags before
  `Handler()`. A nil allowlist admits everything. Only hand-built Servers in
  tests have one.
- Not guarded: the unix socket (`-local-dev`), the in-process host and
  `tw dev` (fakemgmt, which has its own loopback Host check in
  `cmd/tw/devconsole.go`). No browser reaches them.
- Docs: docs/ADMIN-UI.md (the guard, and why plain `:80` exists),
  docs/security-model.md (new trust-boundary row), security/THREAT-MODEL.md
  (B6, the admin's browser), security/LEDGER.md (H-105 row).

## Decisions

- **`:80` stays plain HTTP rather than redirecting to HTTPS.** Hosts and
  `tw link` use it on tailnets without certificates. The Host allowlist
  closes the rebinding gap on both ports.
- **The allowlist is static, built at start.** If you rename the mgmt node,
  restart tw-mgmt (documented in docs/errors.md).
- **No literal `tw-mgmt` unless configured.** It's on the list only when it
  is the hostname, the node's short name or the Service name.

## Tests

- New: `mgmt.TestHostGuardRebinding`. A request with attacker Host and a
  matching Origin is refused with 421 on GET /v1/tenants, GET /v1/audit,
  GET /ui/, POST /ui/tenants/…/delete, DELETE /v1/tenants/… and /mcp, and
  WhoIs is never called. The FQDN, short name, tailnet IPv4/IPv6 and `:443`
  pass, and `/ui/` still renders.
- New: `mgmt.TestBrowserGuardOriginAllowlist`, `mgmt.TestHostAllowlistAllowed`
  and `mgmt.TestMgmtHostNames`.
- `make lint`: pass.
- `make test`:
  - test-js: pass.
  - Go: everything passed except one failure in `hostd`: `TestWorkersKVMove`
    hit a `-race` DATA RACE at `hostd/host.go:403`. That code is untouched
    here and unrelated. Rerunning `go test -race ./hostd -run
    TestWorkersKVMove -count=3` passed. `mgmt`, `cmd/tw` and
    `internal/inproc` pass.
- `make e2e-placement`: pass. It reaches mgmt over the unix socket, so this
  is a regression check only.
- Skipped: the real-tailnet suites (`e2e-real`, `e2e-svc`, `e2e-oauth`,
  `e2e-mac`), which need credentials. They are the only ones that drive the
  tsnet listener end to end.
