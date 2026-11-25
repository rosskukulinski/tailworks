Last update: 2026-11-25 PT. #531: the agent template no longer restates the spawn defaults; the defaults and ceiling are documented.

## What changed

- `js/packages/create-tailworks/templates/agent/tailworks.jsonc`: the live
  `"spawn": { "maxLive": 64, "maxDepth": 3 }` line (equal to mgmt's
  `DefaultMaxLive`/`DefaultMaxDepth`, which mgmt also uses as a ceiling via
  `min(cfg, default)`) is now a comment saying it is the default and the
  ceiling, set only to lower the budget.
- Template README: the `tailnet.spawn` bullet says it lowers the budget
  below 64 live / depth 3 and can't raise it.
- `schema/tailworks.schema.json`: `maxLive`/`maxDepth` get `default` and a
  description (default and ceiling, 0 or absent means the default). No
  `maximum`: `tw check` enforces only `minimum`, and mgmt clamps rather than
  rejects, so a maximum would be a separate behaviour change.
- `docs/tailworks-jsonc.md`: the two rows say the same.
- `docs/errors.md` `spawn_budget_exceeded` Fix: no longer says "raise the
  budget"; the ceiling is 64 / 3.
- `docs/limits.md` unchanged (already correct).

## Tests

- `go test ./schema/... ./docs/...`, `go test ./cmd/tw -run 'TestTypesTemplates|TestInit|TestTemplate'`: pass.
- `make lint`: pass.
- `make templates-typecheck test-sdk`: pass.
- `make demo-test`: PASS.
- `make test` (on mbp via mbp-test.sh): all pass except
  `hostd TestFenceClosesOpenWebSockets` (timing under load);
  `go test ./hostd -run TestFenceClosesOpenWebSockets -count=1` passes on
  rerun. `make test-js` (rerun locally since test-go stopped make): pass.
