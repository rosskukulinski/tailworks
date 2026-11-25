Last update: 2026-11-25 PT. tw dev honours wrangler `dev.port` (#473).

## What's built

- `cmd/tw/dev_port.go`: `resolveDevPort` picks tw dev's port: `--port`
  (explicit, fails when busy), else wrangler `dev.port` (explicit, fails
  when busy, the error names the file), else 8787, falling back to a free
  port with one stderr line when 8787 is busy. `dev.port` is read from the
  config file as written, since `dev` is top-level only (any `--env`).
- `wranglerKeyNote("dev")` no longer warns about `dev.port`; other `dev.*`
  keys (`ip`, `local_protocol`, `host`, …) are named as ignored (tw dev
  binds 127.0.0.1 only, on purpose). An invalid `dev.port` is warned about.
- The busy-port error's fix adds `tw dev --port 0`.
- Docs: `docs/dev-debugging.md`, `docs/wrangler.md` table, `docs/cli.md`.

## Tests

- `make lint`: pass.
- `make test`: the mbp offload ran `./netd/... ./internal/... ./cmd/... ./mgmt/...`
  (all pass) before it hit its 30 min limit (mostly waiting for a slot); the rest
  (`./hostd/... ./security/... ./guest/... ./agent/... ./docs/... ./schema/... .`
  with -race, and `make test-js`) ran locally: all pass.
- `make e2e-wrangler`: pass (TestDevWrangler, TestDevViteRedirect).
- Manual, with 8787 held by another process: `tw dev --once` fell back to a
  free port with one stderr line; `--port 8787` failed with the `--port 0`
  fix; `dev.port: 8787` failed naming wrangler.jsonc; `dev.port: 18899` plus
  `dev.ip` served on 18899 and warned only about `dev.ip`.
- `make e2e-compat`: skipped (needs tailnet credentials, ~60 min).
