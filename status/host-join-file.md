Last update: 2026-11-25 PT. #488: tw host add's join file carries host ID, mgmt URL, control URL and key-tags-only; tw-hostd -join-token-file <f> needs no other flags.

## What changed

- `tw host add` (and tw up's own host join file) writes `TW_HOST_ID`,
  `TW_MGMT_URL`, `TW_TSNET_CONTROL_URL` and, on Headscale,
  `TW_TSNET_KEY_TAGS_ONLY=1` next to `TS_AUTHKEY` and
  `TW_REGISTRATION_SECRET` (cmd/tw/host_joinfile.go). It prints a
  one-command runbook (`tw-hostd -join-token-file <file>`) and a systemd hint.
- `POST /v1/hosts` answers `mgmtURL` (`http://` + mgmt's own MagicDNS
  name) and `keyTagsOnly` (operator tenant in mint mode headscale), so
  the file is right from a `tw link`ed machine (mgmt/hostjoin_url.go).
- tw-hostd resolves host ID, `-mgmt`, `-tsnet-control-url` and
  `-tsnet-key-tags-only` per setting: flag > join file (when it exists) >
  `<tsnet-dir>/join.env` saved after mgmt answered > default
  (internal/hostdrun/joinenv.go). The control URL defaults to Tailscale's
  only when the node has no state; with state and no known URL the hard
  error stays. `-mgmt` stays required when nothing names it. The "hostd
  running" log line has `settings=host=… mgmt=… control=…`
  (flag | join-file | saved | default).
- `tw-hostd join --token-file` takes host ID, control URL and
  key-tags-only from the file (flags win) and saves join.env.
- Docs: hosts.md, INSTALL.md, security-model.md, cli.md, mgmt-api.md.

## Tests

- `go test ./internal/hostdrun -run TestJoinSettingsResolve`: pass (new
  file → no flags needed; old two-key file → -mgmt required; flags win;
  saved join.env; state without control URL → hard error).
- `go test ./cmd/tw -run TestHostJoinFileContent`: pass.
- `go test ./mgmt -run TestTailnetAuth`: pass (mgmtURL in the answer).
- `make api`, `make cli-docs`, `make changes-check` (fragment, `protocol`).
- `make lint`: pass; `make test` (mbp-test.sh): pass, after merging origin/master.
- `make e2e-headscale`: pass (both layouts).
- Skipped: `make e2e-svc` (needs a real tailnet with Services) and
  `make test-pg` (no store change).
