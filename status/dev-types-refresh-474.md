Last update: 2026-11-25 PT. Merged origin/master: `tw types --remote` is gone (#628), so the --remote header logic is dropped. tw dev refreshes a tw-generated worker-configuration.d.ts (#474).

## What's built

- `cmd/tw/dev_types.go`: `devRefreshTypes` runs on tw dev's start (before
  the first `snapshot()`) and after each reload's config re-read (then
  re-snapshots, so its write is no edit). It rewrites
  `worker-configuration.d.ts` only when the file exists, is regular, its
  first line starts with plain `tw types`' header, it has no
  `// Environment: --env` line, and `genTypes` output differs. Skipped
  under `--env`, `--wrangler`, `--from-wrangler-out` and `--once`.
  Errors are warnings on stderr.
- Docs: `docs/wrangler.md` "Types: `tw types`".

## Tests

- After merging origin/master (no more `--remote`): `make lint` pass, `go test ./cmd/tw` pass.
- `make lint`: pass.
- `make test` (on mbp via mbp-test.sh): all pass except two load-timing
  flakes, both passing on rerun: `TestCLISingleProcess` (kill took 1.11 s
  vs a 1 s bound) and `TestTenantAdminScope` (host heartbeat missed).
- `go test ./cmd/tw -run 'TestDevRefreshTypes|TestTypes'`: pass.
- Manual: `tw dev --port 0` on a `hello` app, added a `kv_namespaces`
  binding: the d.ts gained `CACHE: KVNamespace;`, one reload only.
- `make e2e-compat`: skipped (needs tailnet credentials, ~60 min).
