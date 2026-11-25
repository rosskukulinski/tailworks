Last update: 2026-11-25 PT. Issue #524: tw d1/kv/r2/secret surface loadApp's error.

## What changed

- `cmd/tw/appworkload.go`: `appWorkloadErr` / `appWorkload`. When a data
  command needs the workload from the app in the current directory and
  `loadApp` fails, it says "no app in the current directory" only when the
  directory has no app config (no `tailworks.jsonc`, wrangler config or
  `.wrangler/deploy/config.json`). Otherwise it returns loadApp's own error
  (schema problem, bad wrangler config, ...) plus the fix line
  `tw <cmd> <sub> … --workload <workload>   (to skip the app in this directory)`.
- loadApp stays the single source of the workload name (the "simpler"
  verifier's form); the base-name precedence (tailworks.jsonc name >
  wrangler name > directory) moved out of `loadWrangler` into `appBaseName`.
- Call sites in d1.go, kv.go, r2.go, secret.go: one line each.

## Tests

- `go test ./cmd/tw -run 'TestAppWorkload|TestAppBaseName|TestCLISecret|TestCLID1|TestCLIKV|TestCLIR2|Wrangler|Vite|Pages'`: ok
- `make lint`: ok
- `make test` (mbp-test.sh): cmd/tw and all others ok except
  `mgmt TestWorkloadReplicas` (no eligible host: not live; unrelated, this
  branch doesn't touch mgmt); rerun locally `-count=2`: ok.
