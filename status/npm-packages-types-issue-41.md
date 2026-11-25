Last update: 2026-11-15 PT. Issue #41: publishable npm packages, typed templates, `twd types`.

## Built

- **`@twd/tailnet` global types** (`js/packages/tailnet/src/globals.ts`):
  `ctx.tailnet?: TailnetContext` on `DurableObjectState<Props>` and
  `ExecutionContext<Props>` (type parameters match workers-types). Loaded by
  `import "@twd/tailnet"` or tsconfig `"types": ["@twd/tailnet"]`.
- **Templates:** each has a `tsconfig.json` (workers-types + `@twd/tailnet`;
  node-workflow: `@types/node`), devDependencies `@cloudflare/workers-types`
  5.20261114.1 (matches the workerd pin) and `typescript` 7.0.2, and a
  generated `worker-configuration.d.ts`. Two small source fixes the type
  check found: `lock/src/client.ts` (`clearTimeout(undefined)` under
  workers-types) and `mcp/src/index.ts` (`type: "text" as const`).
  Template edits stay off the `"scripts"` line that PR #49 changes.
- **`twd types [dir] [--env] [--out] [--check] [--remote]`** (`cmd/twd/types.go`):
  `Env` from vars, DO/KV/R2/D1/Queue-producer/Workflow bindings and secret
  names (`.dev.vars`, `.dev.vars.example`, `secrets.required`, `--remote` =
  mgmt's list). Node apps get `NodeJS.ProcessEnv`. `--check` fails with the
  new code `types_stale` (docs/errors.md). Schema def `types` in
  schema/cli.schema.json; cliSurface entry; docs/wrangler.md "Types".
  Wrangler `secrets` is no longer warned about as "ignored".
- **npm publishing (not run):** package metadata (repository, homepage,
  bugs, keywords, `publishConfig.access: public`, `prepack` builds dist +
  .d.ts). `scripts/npm-versions.mjs` (check, or `--set <v>`) with
  `TestNPMVersions` in `make test`. `scripts/npm-publish.sh <v> [--dry-run]`
  (dependency order, skips already-published, prerelease → `next`).
  `.github/workflows/npm-publish.yml` on `release: published` (ADR-041 §9:
  npm after the GitHub release), skipped without `NPM_TOKEN`. release.yml
  gates run the publish `--dry-run`. docs/RELEASING.md steps 3 and 8.
- **`npm create twd`:** create-twd's bin runs `twd init` when twd is on PATH,
  else copies the template.
- `make templates-typecheck` / `scripts/templates-typecheck.sh`: the
  acceptance test without the registry.

## Test results

- `make lint`: ok (check-changes: needs cli-json, found in the fragment).
- `make test`: ok (41 Go packages ok, JS suites fail 0).
- `sh scripts/templates-typecheck.sh`: all 7 templates ok (twd init, npm
  install with @twd/* from npm-pack tarballs, `npx tsc --noEmit`,
  `twd types --check`).
- `sh scripts/npm-publish.sh 0.2.0 --dry-run`: all four packages pack and
  validate.

## Gaps / maintainer

- First real publish: own the `@twd` npm scope and `create-twd`, add the
  `NPM_TOKEN` secret, then publish (workflow or by hand).
- The repo has no LICENSE file; packages declare BSD-3-Clause only.
- Templates still declare their own module-local `interface Env`; the
  generated global `Env` is there for new code.
