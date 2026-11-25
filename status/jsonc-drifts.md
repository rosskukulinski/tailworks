Last update: 2026-11-24 19:50 PT. #440 (tailworks.jsonc schema/doc drifts) and #447 (duplicate `name` in the wrangler templates) done in one branch.

# jsonc-drifts

## What changed

#440:

- `schema/tailworks.schema.json`: `tailnet.egress.via` (`enum: ["exit-node"]`), so `tw dev` / `tw deploy` accept what hostd and mgmt already read; `ttl` is "lifetime from creation; the default for spawn()"; `containers` is "per-class image refs you pin; mgmt records the digests it resolves in the deployment"; `placement.host`, `hostname`, `region` carry `"deprecated": true` (schema.go ignores the keyword, so they still pass); `node`/`egress` descriptions say `"node": false` implies workload egress.
- `schema/structs.go`: `Unlisted(reflect.Type)`, the json-tagged fields of a config struct the schema has no property for. `hostd/bundle.TestTwConfigKeysInSchema` and `mgmt.TestTwConfigKeysInSchema` require it empty (the bundle test fails against the old schema, on `tailnet.egress.via`).
- `internal/publicspec`: `Effective()` derives an unset `burst` as `min(Ceilings.Burst, max(Defaults.Burst, ceil(2*rps)))`, same for `ipBurst` from `ipRps`. It only grows a burst, and an explicit one is kept.
- Wording: docs/tailworks-jsonc.md (containers row, `node`/`egress` rows, placement table down to `mode`/`targets` plus one sentence), docs/INSTALL.md (the commented `aggregate` line is an "Advanced" sentence), docs/egress-modes.md and the mcp template (`"TwBudget": { "node": false }`), docs/public-ingress.md (opt-in example without the default `limits`, one paragraph under the Limits table), hostd/bundle/bundle.go and internal/container/container.go comments and one error message, mgmt/service.go's `namespace %q needs tags (or "node": false ...)`.

#447:

- Dropped `"name"` from the tailworks.jsonc of the eight wrangler templates (agent, ai-agent, hello, lock, mcp, python, refund-approval, static-site; python was added after the audit counted seven). node-workflow keeps it, since it has no wrangler config. The schema key and the precedence are unchanged.
- `cmd/tw/appname.go`: two `›` notes. One when tailworks.jsonc and the wrangler config set different names (tailworks.jsonc wins, as before; no note with `--name`). One when `tailnet.tags` is exactly `tag:tw-wl-<other>` and `<other>` is none of the app's names (workload, tailworks.jsonc, wrangler, directory), as when an app is copied and renamed. `--env`'s `<name>-<env>` keeps the base tag without a note.
- `--name` help on `tw deploy` and `tw policy`: "tailworks.jsonc name, then wrangler name, then the directory"; `make cli-docs`.

## Tests

- `make lint`: ok.
- `make test`: failures in netd `TestService`, mgmtd `TestStartCloseBudget`, cmd/tw `TestCLIOAuth`, four mgmt tests (`no eligible host ... not live`) and the hostd/gen Python tests (`workerd did not come up`). The machine's load average was about 95 during the run. Each failing test passed when rerun alone: `go test ./netd -run TestService`, `./internal/mgmtd -run TestStartCloseBudget`, `./cmd/tw -run TestCLIOAuth`, `./mgmt -run 'TestRT3_H23_PlacementPoison|TestRedeployAfterUndeploy|TestRT_Hr2group_ReplicaNameReserved|TestTenantAdminScope'` and `./hostd/gen -run TestPython`.
- After merging origin/master (`efef68ba`): `make cli-docs` (no change), `make lint` ok, `make test`: `TestCLIOAuth` and `mgmt.TestWorkloadReplicas` (`no eligible host ... not live`) failed at load average 80. Both passed when rerun alone.
- `make templates-typecheck test-sdk`: ok.
- `make e2e-wrangler`: ok.
- `make demo-test`: it printed "SKIPPED (no docker daemon)".
- `tw types` in each changed template: no changes.
- `make e2e-compat`: skipped (no Docker here).
