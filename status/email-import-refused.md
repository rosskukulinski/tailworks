Last update: 2026-11-24 15:00 PT. The built-in bundler refuses the app's own `cloudflare:email` imports with `not_supported`; dependency imports and pre-built Workers keep the stub.

# email-import-refused

## Decision

Ross: a Worker must not be able to import `cloudflare:email` until tailworks
implements email. Refusing every import was ruled out: `agents` 0.33.0
imports `cloudflare:email` at the top of its main chunk
(`node_modules/agents/dist/src-*.js`), so the `agent` and
`refund-approval` templates (the launch demo) and every Agents SDK app
would be refused. He chose option 1: refuse only the app's own imports.

## What changed

- `internal/emailstub/refuse.go`: the rule and the message.
  `cloudflare:email is not supported on tailworks yet (imported by <file>)`,
  code `not_supported`. "Own code" is an importer whose real path has no
  `node_modules` directory. esbuild resolves symlinks, so a workspace or
  `npm link`ed package counts as the app's own code (the user can edit it).
  Packages under `node_modules`, including tailworks' own NodePaths, are
  dependencies and keep the stub.
- `internal/esbundle/email.go`: the resolve hook. It stubs a dependency's
  import, and refuses and records an own import. `BuildApp` returns an
  `emailstub.ImportError` that lists every importer, relative to the app
  directory. `build.mjs` applies the same rule and text, and
  `TestBuildMJSMatches` checks the text.
- `cmd/tw/emailimport.go`: maps the error to `not_supported` with a fix,
  through one call in `buildEsbuild`.
- Pre-built (`no_bundle`, Vite) and `--wrangler` Workers are unchanged: they
  keep #375's workerd-config stub (`hostd/gen/email.go`), because tailworks
  can't tell whose import it is there. There is no refusal at pack time and
  none in hostd.
- `e2e/sdk` cfagents: the probe's own `import("cloudflare:email")` would now
  fail the app's build, so it is gone. `UnsupportedBindings` now checks
  (`e2e/sdk/email_test.go`) that the cfagents bundle carries the stub for
  the `agents` import, and that a dynamic own import is refused with the
  file named.
- docs/errors.md `not_supported` (When/Fix), the docs/COMPAT.md email rows,
  `changes/email-import-refused.md` (kind breaking).
- The ag-playground expectation is unchanged: it is Vite-built, so it gets
  the stub.

## Tests

- `go test ./internal/esbundle ./internal/emailstub ./cmd/tw -run 'Email|Dependency|BuildMJS'`:
  pass. `TestEmailImportRefused` (static, dynamic and entry imports, in the
  Go bundler and build.mjs) and `cmd/tw:TestEmailImportRefused` fail on
  origin/master: the build succeeds, or emailstub has no refusal API.
- `make lint`: pass.
- `make test` (mbp via mbp-test.sh): pass.
- `make templates-typecheck test-sdk`: pass (UnsupportedBindings passes).
- `make e2e-wrangler`: pass.
- `make e2e-compat-dev`: PASS 17, XFAIL 3 (ag-cross-domain and
  ag-mcp-elicitation: #355; ag-playground: RpcPromise). These are the same
  results as recorded. I didn't run `make compat-upstream`, because no
  outcome changed and it would only re-stamp the commit hash.
- Skipped: `make e2e-compat` (needs a real tailnet).
