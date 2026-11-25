Last update: 2026-11-25 PT. #515: the admin UI checks `Who.Admin` alone; no `Kind == "local"` branches.

## What

- `mgmt/ui/ui.go` `base()`: `/v1/status` only when `Who.Admin` (comment
  records that a zero `Who`, i.e. whoami failed and the error page renders,
  now skips it and shows the approvals-only nav). `overview()`:
  `operator := d.Who.Admin`.
- `templates/layout.html`: nav is `or .Who.Admin .Who.TenantGrants`.
- `docs/ADMIN-UI.md`: mgmt's own node folded into the admin row, keeping
  the trust statement (admitted as an admin without any grant).
- `mgmt/ui/localnode_test.go`: whoami `{"kind":"local","admin":true}` gets
  the operator overview (`/v1/status` and `/v1/hosts` called).
- Non-UI `Kind == "local"` checks (rate limits, all-tenant scope) untouched.

## Only observable change

On the error page rendered when `/v1/whoami` itself fails, the UI no
longer calls `/v1/status` and the nav shows only Approvals.

## Tests

- `go test ./mgmt/ui/ ./mgmt/ -run 'UI|Whoami'`: ok
- `make lint`: ok; `make changes-check`: ok (no upgrade-relevant paths)
- `make test` (on mbp via mbp-test.sh): all ok except
  `mgmt TestStalePoolNodesKilledOnMove` (503 no_host, "h2 is not live (no
  heartbeat within 10s)" under load); unrelated, passes `-count=3` locally.
