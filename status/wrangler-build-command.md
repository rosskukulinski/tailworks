# wrangler-build-command (issue #495)

Runs the wrangler config's `build.command` natively from the built-in
esbuild path (owner decision on #495): `cmd/tw/buildcmd.go`.

- `tw deploy` / `tw policy`: `a.build` runs it first (shell in `build.cwd`,
  `node_modules/.bin` on PATH), prints `› build      <command>`, streams
  output on stderr; failure is `build_command_failed` (docs/errors.md).
- `tw dev`: runs it at start and only when `build.watch_dir` (default
  `./src`) changes; the change snapshot is re-taken right after a build, so
  output under `assets.directory` is not an edit (no reload loop;
  `TestDevBuildNoLoop`).
- Not run for `--wrangler` / `--from-wrangler-out` (wrangler owns it), Pages,
  pre-built `no_bundle`, `runtime: "node"`. `rules` / `no_bundle` stay on
  `--wrangler`. No auto-selection of wrangler.
- e2e/compat: `--wrangler` dropped from durable-chat and multiplayer-globe.
- Docs: wrangler.md (#custom-builds), security-model.md (project code runs on
  the deploying machine), COMPAT.md row, errors.md.

## Tests

- `go test ./cmd/tw -run 'Build|Wrangler|DevBuild|Assets|Pages'`: pass.
- `make lint`, `make lint-docs`, `make changes-check`: pass.
- `make test`: everything passes except timeouts under a load average of
  100-170 (cmd/tw TestDevPython/TestCLIOAuth/TestCLIEndToEnd, and mgmt and
  hostd deploy/workerd-health timeouts, none touching this change).
  TestCLIEndToEnd passed alone; TestDevPython fails alone with "workerd did
  not become healthy" (Pyodide start under load; no build key involved).
- `make e2e-wrangler`: pass.
- `TW_COMPAT_ONLY='^(durable-chat|multiplayer-globe)$' make e2e-compat-dev`:
  both PASS without `--wrangler`.
- Manual `tw dev` on durable-chat (public/dist deleted first): the build ran
  once, 0 reloads in 40 s idle; touching a src/client file gave exactly one
  rebuild and one reload.
- Skipped: `make e2e-compat` (deployed; needs a real tailnet and credentials).
