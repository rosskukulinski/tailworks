Last update: 2026-11-25 PT. Merged origin/master (after #620, #628, #633, #637); resolved cmd/tw/app.go and dropped --keep-tags/APP_NAME from create-tailworks.

## Merge with master (2026-11-25)

- `cmd/tw/app.go` conflict: master (#628) removed `--keep-tags` and this
  branch removed the `APP_NAME` regex; kept both removals.
- create-tailworks (#620): removed `--keep-tags` (no longer a `tw init`
  flag) from the flag parser, the pass-through, the "needs tw" message,
  `docs/INSTALL.md` and the README; `lib/init.mjs` no longer renames
  `APP_NAME` (no template has it now).
- After the merge: `make lint` pass; `go test ./cmd/tw` pass (includes the
  create-tailworks vs tw init drift test); create-tailworks `npm test`
  7/7; `make templates-typecheck test-sdk` pass.

# sdk-self-workload (issue #471)

## Done

- `ctx.tailnet.self()` answers `workload` and `deploymentId` (hostd/http.go
  `self` op; `TailnetSelf` type in @tailworks/tailnet; testing fake defaults
  `test-workload` / `test-deployment`). `make gen-js` rebuilt tw-rt.js.
- refund-approval: the `tw approve <app> <id>` hint reads
  `self().workload` once (guarded: `<app>` without ctx.tailnet); `vars.APP_NAME`
  gone from wrangler.jsonc and `tw types`; tw init's APP_NAME regex and
  `TestInitRenamesAppVar` deleted. `TestRefundApprovalTemplate` now checks
  the hint.
- `Caller.actingFor`/`chain` and `TailnetContext.forward` removed from the
  types (and getCaller's object); the host runtime keeps `forward` as an
  unlisted alias for one minor. @tailworks/mcp: no `chain` in error
  details, a `groups[tool]` grant denies fail-closed (same message),
  `principalKey` = user login else nodeId.
- Docs: headscale.md, testing.md, RFC.md:42 (status table).
- node-workflow template: `node.health` line removed (the `/healthz` route
  stays). node-tier.md and the schema say any answer on `/` counts; the
  schema's `listen` description and node-tier.md name the cost of
  `listen: "unix"` (AF_UNIX stays open; host not shareable). The mode is kept.
- `changes/sdk-self-workload.md`, kind breaking, with the migration.

## Tests

- `make lint`: ok
- `make templates-typecheck`: ok (all templates)
- `make test-sdk`: ok (incl. TestRefundApprovalTemplate)
- `make test-vitest`: ok
- `make demo-test`: PASS
- `node --test` in js/packages/tailnet (51) and js/packages/mcp (5): ok
- `go test ./cmd/tw -run 'TestInit|TestTypes|TestTemplate'`, `go test ./schema/...`: ok
