Last update: 2026-11-25 12:00 PT. #519: hostd-additions.md is now hostd-api.md with a current route table.

## What changed

- `docs/contracts/hostd-additions.md` renamed to `docs/contracts/hostd-api.md`
  and rewritten: it opens with one table of tw-hostd's 14 routes (13 under
  `/v1` plus `GET /metrics`), with method, path, surfaces (local socket,
  tailnet push port, in-process), caller (mgmt, naming the `tw` command
  behind it, or the `tw` command itself) and purpose, built from
  `hostd/http.go` (`pushHandlers`, `pushAPI`). The v0-era sections
  (what hostd sends, optional fields, M2 gaps, M4 status) are gone:
  mgmt-api.md covers them. Push authentication, the self-upgrade push, the
  embedded host and the platform-wrapper sockets are kept; their stale
  route lists now point at the table.
- `api/openapi.yaml`'s "hostd's API" sentence names the mgmt → hostd calls
  and the operator's socket calls and links hostd-api.md (`make api`).
- `hostd/http.go`: comments on `pushHandlers`, `tsnetPushAPI` and
  `PushHandler` said "kills and assignments only"; they now describe the
  full set. No code change.
- `docs/README.md` and `llms.txt` link the new file.
- `changes/hostd-api-doc.md` (`fix`, `protocol`: description text only).

No route moved (ADR-041).

## Tests

- `make lint`: pass.
- `make test` (mbp, via mbp-test.sh): all pass except `cmd/tw TestCLIOAuth`
  and `hostd TestFenceClosesOpenWebSockets`, both timing failures under
  load; each passes on a local rerun (`go test -race -run …`). Neither
  touches anything this branch changes (docs and comments only).
- `make changes-check`: ok.
