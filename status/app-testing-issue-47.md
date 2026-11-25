Last update: 2026-11-15 13:40 PT. Testing story for twd apps (issue #47): vitest-plugin measured, @twd/tailnet/testing, twd dev --caller + startDev, a test per template.

# App testing (issue #47)

## Built

- `@twd/tailnet/testing` (`js/packages/tailnet/src/testing.ts`):
  `withCaller(req, {login | tags, caps, …})`, `withPublic(req, {ip})`,
  `createTestTailnet({self, fetch, onSpawn, env, bindings})` → a fake
  `ctx.tailnet` (`spawned`, `killed`, `audits`, `fetches`, `ctx()`,
  `attach(state)`, `settle()`). Top-level `withCaller` throws unless
  `enableTestCallers()` or `createTestTailnet()` ran.
- `twd dev --caller <name>[=<tag>,…]` (`cmd/twd/dev_callers.go`,
  `netd/nettest/tagged.go`): one extra dev node + local proxy per identity;
  `callers` in the ready event and the `--once` result (schema updated).
- `@twd/tailnet/testing/dev` (`src/testing-dev.ts`): `startDev()` spawns
  `twd dev --port 0 --json`, waits for `ready`, `stop()` = SIGTERM then wait
  (SIGKILL after 15 s); kills leftover children on process exit.
- Each template: `test/app.test.js` (node:test, black box via `startDev`)
  and `"test": "node --test test/*.test.js"`; node-workflow also gets
  `@twd/tailnet` as a devDependency.
- `e2e/vitest` + `make test-vitest`: the lock, agent, mcp and
  refund-approval templates under `@cloudflare/vitest-plugin` 1.8.2 /
  vitest 5.1.0 with `@twd/tailnet/testing`.
- Fix: `twd dev` took its change snapshot after emitting `ready`, so an edit
  made right after `ready` could be missed (`TestDevJSONEvents` timed out
  consistently on this branch until fixed).
- Docs: `docs/testing.md`, COMPAT row `vitest-plugin` (⚠️) + README row,
  `docs/cli-json.md`, `llms.txt`.

## vitest-plugin findings (1.8.2, vitest 5.1.0)

- Works unchanged with twd templates' `wrangler.jsonc`: Worker fetch,
  `exports.default.fetch`, DO KV/SQLite storage, alarms
  (`runDurableObjectAlarm`), `runInDurableObject`, Agents SDK
  (state, SQL, routeAgentRequest), a Workflow instance starting and waiting,
  createMcpHandler + @twd/mcp caps/limits/budget DO.
- `new Request(url, {cf})` sets `request.cf` in workerd, and it survives
  `exports.default.fetch` and a DO stub's `fetch()`; a `cf` defined on the
  instance with `defineProperty` does not cross a stub (lost). So
  `withCaller` uses the init form, with defineProperty only as the Node fallback.
- `ctx.tailnet` is absent and `getCaller` throws `no_identity` without the
  helper. A fake attached to a DO's state via `runInDurableObject` persists
  for that instance.
- miniflare ships its own workerd build (not twd's pin).
- Not tested: hello, ai-agent (needs an MCP server via ctx.tailnet.fetch),
  node-workflow (Node tier, not applicable).

## Test results

- `npm test -w @twd/tailnet`: 32/32 pass (6 new in `test/testing.test.js`).
- `go test ./cmd/twd -run 'TestDevCallerFlag|TestDevCallers|TestTemplateTests|TestCLIJSONContract|TestDevJSONEvents'`: pass
  (TestTemplateTests runs `npm test` in copies of hello, lock, agent, mcp,
  ai-agent, refund-approval; ~19 s).
- node-workflow template test: passes by hand with e2e/node's deps
  (`make test-node` installs them); not in TestTemplateTests.
- `cd e2e/vitest && npx vitest run`: 4 files, 9 tests pass.

## Gaps / next

- `twd dev` can't choose a caller's login (testcontrol names users) and has
  no policy evaluator, so allowed-under-a-grant paths need unit tests.
- No `withCaller` for the Node tier (`@twd/node`'s signed header).
- `startDev` is Node-only; no in-process library form of `twd dev`
  (wrangler `unstable_startWorker`-style) beyond the subprocess.
