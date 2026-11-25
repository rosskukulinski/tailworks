Last update: 2026-11-24 PT. `tw secret bulk` (#460) and one subcommand scanner for notify/telemetry (#468).

# simplify-secret-bulk

Issues #460 and #468 from the interface-simplification audit (#434).

## What's built

- **#468** `scanVerbOr` (cmd/tw/verbs.go): `scanVerb` for a one-word
  subcommand that defaults when none is given. `tw notify` and
  `tw telemetry` use it (default `show`) instead of their own copies of a
  scanner that knew only `--tenant` and `--state-dir`, so any flag with a
  value (`--header X`, `--webhook -`) can come before the verb. Their
  set-only flags are registered for `set` only (`notifyFlags`,
  `telemetryFlags`), so `tw notify --help`, `tw notify rm --help` and
  docs/cli.md stop listing them; the "`--webhook … are for tw notify set`"
  guards are gone (another verb now gets the usual unknown-flag
  `bad_request`).
- **#460** `tw secret bulk [<file>]` (cmd/tw/secretbulk.go): dotenv or a
  JSON object of strings (sniffed by the first non-space character), from
  the file or piped stdin (no file, or `-`).
  - Refuses an argument containing `=`, extra arguments, and a terminal on
    stdin with no file, without echoing anything; a missing file's error
    leaves out the path (it might be a mistyped value).
  - Checks before the first PUT: UTF-8, syntax, names, duplicates (named,
    with both lines), value sizes, count, and the workload's total after
    the run (one list call).
  - JSON is read with the token decoder, so its errors name a line, never
    the decoder's message; non-string values are refused by name.
  - Sequential PUTs with `Tw-Actor`, the same route as `tw secret put`.
    On a failure: the cause's code, a message naming the secret, and
    detail lines `set:` / `failed:` / `not attempted:`; exit non-zero.
    `--json` on success: `{workload, tenantID, set, failed, notAttempted}`.
  - PUT-only: never deletes names absent from the input (docs say so).
  - `parseDotenv` takes the file name for its errors (it hardcoded
    `.dev.vars`; `tw types` no longer rewrites the message), and
    `dotenvVars` returns the pairs in order with their line numbers.
  - `secretWorkload` extracted from `cmdSecretSub` for both.
- Docs: docs/secrets.md (`tw secret bulk` section; no longer under "Not
  supported"), docs/COMPAT.md (supported; sequential, not atomic, listed
  as a difference), ADR-034 §6 (file or piped stdin, never argv), the
  tw-secrets skill (the user runs `tw secret bulk .dev.vars`),
  schema/cli.schema.json `$defs/"secret bulk"`, `cliSurface`,
  docs/cli.md (`make cli-docs`), changes/simplify-secret-bulk.md.

## Tests

- `go test ./cmd/tw -run 'TestReadSecretBulk|TestSecretBulkArgs|TestPutSecretsInTurn|TestCLISecretBulk|TestNotifyTelemetryVerbs|TestCLISecret$|TestCLINotify|TestCLITelemetry' -count=1 -v`: pass.
- `go test ./cmd/tw -count=1`: ok (20 s, includes TestCLIJSONContract).
- `make lint`: pass (after merging origin/master).
- `make test` on a very heavily loaded machine: every package ok except
  timing failures in packages this branch doesn't touch
  (`internal/mgmtd` TestStartCloseBudget, `mgmt` TestWorkloadReplicas /
  TestTenantAdminScope "host not live", `hostd` TestR2Binding,
  `hostd/gen` TestPythonAssets and `cmd/tw` TestDevPython "workerd did not
  come up"). Each passes rerun on its own (`go test ./<pkg> -run <test>
  -count=1`).

## Not done / judgment calls

- No warning for a group/other-readable secrets file (optional in the
  issue; most `.dev.vars` are 0644, so it would fire on nearly every run).
- e2e/compat still loops `tw secret put`; it works and exercises the
  single-secret path.
- Mid-run failures can't be produced against a real mgmt once the
  pre-checks pass, so `putSecretsInTurn` is tested with a fake PUT.
