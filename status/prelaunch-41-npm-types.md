Last update: 2026-11-17 PT. Issue #41 follow-up: templates use the generated `Env`; packages ready for a first npm publish.

Continues `status/npm-packages-types-issue-41.md` (which built the
tsconfigs, `tw types`, `scripts/npm-publish.sh` and the npm-publish
workflow). This branch closes the gaps that file and the 2026-11-16
critique listed.

## Built

- **Templates use the generated `Env`.** The module-local `interface Env`
  in hello, agent, mcp, ai-agent, lock (`index.ts` and `lock.ts`) and
  refund-approval is gone, along with `type DurableObjectNamespace = any`
  and the `any` bindings. Each file is an ES module, so the local interface
  shadowed the global one `tw types` writes to `worker-configuration.d.ts`.
  Now `env.*` is typed from the config (for example
  `DurableObjectNamespace<SupportAgent>`, `Workflow`).
- **`TestTemplatesUseGeneratedEnv`** (`cmd/tw/types_test.go`, in
  `make test`): fails when a template's `src/*.ts` declares its own `Env`.
- **npm package contents:** a `README.md` (the npm page) and the BSD-3-Clause
  `LICENSE` in `@tailworks/tailnet`, `@tailworks/mcp`, `@tailworks/node`
  and `create-tailworks`. Before this, the tarballs had neither.
- **Docs:** `docs/wrangler.md` (templates rely on the global `Env`; npm 404
  caveat until the first publishing release) and `docs/testing.md` (`npm
  test` needs `npm install`, same caveat). `docs/RELEASING.md` step 8 has
  the one-time npm setup: create the `tailworks` npm org, check that
  `create-tailworks` is free, the `NPM_TOKEN` secret, publish from a
  post-rename tag only (`v0.1.0` has the old `@twd` names), check, then
  drop the caveats.

## Test results

- `make lint`: ok.
- `make changes-check`: ok (no upgrade-relevant paths changed).
- `make test`: Go packages ok except `netd` `TestEgressWaitsForNewPeerPath` (timing, 5.2s vs a 5s bound under load from parallel worktrees; netd untouched here; passed on rerun with `go test ./netd -run TestEgressWaitsForNewPeerPath`). `cmd/tw` ok incl. `TestTemplatesUseGeneratedEnv`. `make test-js`: ok (fail 0 in all three suites).
- `make templates-typecheck`: all 7 templates ok (npm install from packed
  tarballs, `npx tsc --noEmit`, `tw types --check`).
- `sh scripts/npm-publish.sh 0.2.0 --dry-run`: all four packages pack,
  each with LICENSE and README.md.
- `make test-sdk`: ok (TestAgentTemplate, TestRefundApprovalTemplate, TestCloudflareAgents, TestVercelAI, …).
- Not run: `make e2e-wrangler` (no bundling or wrangler config change).

## Gaps / maintainer

- Not published: the `@tailworks` npm org is unclaimed and there is no
  `NPM_TOKEN`. Steps are in `docs/RELEASING.md` step 8, "One-time setup".
- Until then, `npm install` in a `tw init` app 404s (documented). `tw dev`
  and `tw deploy` work without it for workerd templates; `node-workflow`
  needs `npm install`, so it can't be built outside this repository yet.
- `lock/src/lock.ts` still extends `(DurableObject as any)` with
  `ctx: any`; typing it fully is a separate cleanup.
