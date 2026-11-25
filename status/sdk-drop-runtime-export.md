Last update: 2026-11-25 PT. Step 1 of #536: `@tailworks/tailnet` no longer exports `./runtime`.

## What changed

- `js/packages/tailnet/package.json`: removed the `./runtime` subpath export.
  It was platform-internal (runtime.ts says tenant code never imports it),
  and nothing in the repo imports `@tailworks/tailnet/runtime`: hostd builds
  `tw-rt.js` straight from `src/` (`make gen-js`), the package's own tests
  import `../dist/runtime.js` by path, and the public types
  (`TailnetContext`, `SpawnOptions`, `Spawned`) reach users through
  `index.ts`. `src/runtime.ts` and `dist/runtime.js` stay.
- `js/packages/tailnet/README.md`: one sentence saying `testing/dev` only
  drives `tw dev`, so it is runtime-agnostic, which is why Node-tier
  templates list `@tailworks/tailnet` as a devDependency.

## Left open

Step 2 of #536 (fold `@tailworks/mcp` into `@tailworks/tailnet/mcp`, a
`kind: breaking` change one minor later, with a re-export shim vendored in
`scripts/release.sh` roots) is not done here; the issue stays open. It
should build on #471's PR, which also edits `@tailworks/mcp`.

## Tests

- `make lint`: pass.
- `make templates-typecheck test-sdk`: pass.
- `make test` (on mbp via mbp-test.sh): everything passed except
  `mgmt TestTenantAdminScope` (`no_host`: host heartbeat missed under
  load); `go test ./mgmt -run TestTenantAdminScope -count=1` passes on
  rerun. Unrelated to this JS-only change.
- `make test-js`: pass (rerun locally because test-go stopped make first).
- `make npm-cli-test`: not run; the npm CLI packages are not touched.
