Last update: 2026-11-24 PT. Browser HTML error pages for mgmt's admission refusals (issue #497).

## What's built

- `mgmt/errors_html.go`: `wantsHTML` (netd.WantsHTML's semantics, copied
  because mgmt does not import netd), `htmlErrPage` (GET/HEAD of `/`, `/ui`,
  `/ui/...` with a browser Accept) and `writeErrHTML`, a standalone
  html/template page: status, code, message, grant snippet in `<pre>`, a fix
  sentence per code, trace ID, doc link (`rel="noreferrer"`). Headers: CSP
  `default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'`,
  `no-store`, `nosniff`, `Referrer-Policy: no-referrer`,
  `X-Frame-Options: DENY`, `Vary: Accept`.
- `writeErr` (mgmt/server.go): one branch after `Tw-Error`/`Retry-After`.
- `HostAllowlist.guard` (mgmt/hostguard.go) attaches the allowlist's names
  to the 421 in an unserialized `Error.hostNames` field; the HTML page shows
  those instead of the message (which echoes the request's Host).
- Docs: `docs/errors.md` header contract and the `misdirected_request`,
  `no_identity`, `not_admin` entries; `docs/ADMIN-UI.md` admission section.

Decisions: no ADR (the issue asks for none; no ADR covers mgmt's error
serialization). The page applies to any error writeErr emits on those
paths (for example `forbidden` for a host), not only the three codes; the
three get a fix sentence. Authorization, status codes and the JSON body for
every non-browser client are unchanged.

## Tests

New `mgmt/errors_html_test.go`: `TestErrHTMLNotAdmin` (HTML on `/`, `/ui`,
`/ui/...`, HEAD; escaped hostile login; JSON unchanged for no Accept, `*/*`,
`application/json`, ties, `/v1`, `/mcp`, POST, `/uix`),
`TestErrHTMLNoIdentity`, `TestErrHTMLMisdirected` (hostile Host never
echoed; mgmt's names listed; CLI JSON unchanged), `TestWantsHTML`.

Commands (Go 1.27.4):

- `go test ./mgmt/... -count=1`: ok.
- `make changes-check`: ok (no upgrade-relevant paths, no fragment).
- `make lint`: ok.
- `make test`: failed in `mgmt`, `internal/mgmtd`, `cmd/tw` and `hostd`
  because the disk filled up during the run ("no space left on device",
  "database or disk is full") at load average ~236 from other agents, plus
  client timeouts. Rerun alone afterwards: `go test ./mgmt/...
  ./internal/mgmtd/ ./docs/ -count=1` ok. `cmd/tw` and `hostd` were not
  rerun (they don't touch the changed code paths beyond mgmt's handler; CI's
  `make test` is the authority).

Skipped: `make e2e-multimgmt` (needs Docker; the change is per-request
serialization only, no shared state). No other suite in the AGENTS.md table
applies.
