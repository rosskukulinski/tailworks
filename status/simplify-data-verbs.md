Last update: 2026-11-24 PT. Data commands: one verb helper, per-verb flags, one subcommand scanner, kv/r2/d1 store names by binding (#458, #461, #462).

## What's built

- `cmd/tw/verbs.go`: `normalizeVerb` (list → ls; remove, delete, del → rm),
  called by d1, kv, queues, r2, secret, tenant, host, release, telemetry,
  notify, image public, docs and agent. `scanVerb` finds a command's verb
  words among flags given before, between or after them, using the
  command's own flag registration (`anyVerb`) to know which flags take a
  value; `onVerb` gates a flag to its verbs. #468 (notify/telemetry) can
  call `scanVerb(args, register, func(string) int { return 1 })` and
  default to `show` when the verb is empty.
- d1, kv, queues, r2: one `<cmd>Flags(f, verb)` function each registers the
  shared flags (`--tenant`, `--workload` where it applies) and the verb's
  own; `--yes` only on the verbs in `destructiveCommands`. Flag sets keep
  the top-level name, so bad-flag errors print the full usage. secret uses
  the scanner (all its verbs share their flags).
- `ls`/`rm` are canonical for d1 and kv (usage lines, hints, docs, skills,
  openapi descriptions); `list`/`delete` stay as aliases.
- `cmd/tw/storename.go` + `hostd/bundle/storebindings.go`: `storeName`
  resolves a store the app declares by that name, else a binding, else the
  literal. kv `--namespace` (and `--binding`, the same) take either, and
  default to the app's only `kv_namespaces` entry when the workload is the
  cwd app's. r2 `<bucket>` and d1 `<db>` use the same resolver (r2 before the
  name check and the delete confirmation).

## Decisions

- Partial verbs (`tw kv key --help`, `tw d1 migrations --help`) print the
  usage only; docs/cli.md lost its `tw kv key` / `tw kv namespace`
  sections, which repeated every flag.
- An unknown verb is reported before flags are parsed (so `tw kv key frob
  --ttl 5` says unknown command, not unknown flag).
- r2 and kv resolve a binding against the cwd app even with `--workload`
  (as d1 and kv `--binding` already did); only kv's no-flag default is
  restricted to the cwd app's own workload.
- `tw agent rm` reaches `remove`; agent's usage line still says `remove`.

## Tests

After merging origin/master (which added `needsSubcommand`, now used by
all five commands):

- `make lint`: ok. `make changes-check`: ok (fragment names protocol: the
  openapi change is description text).
- `make test`: cmd/tw failed only `TestSubcommandHelp` (its expectations
  named list/delete; fixed), then `go test -race ./cmd/tw/` ok. hostd
  failed `TestLockTemplate`, `TestWorkersKV`, `TestContainerPolyfill`,
  `TestPythonQueues`, `TestR2Binding` with timeouts under machine load;
  rerun alone, all pass. An earlier run's mgmt failures
  (`TestWorkloadReplicas`, `TestWarmKeysRevokedOnCloseInParallel`) passed
  rerun alone.
- `make e2e-tw-upgrade`: ok (`TestTwUpgradeSingleMachine`, which drives
  `kv key get/put --binding` through old and new binaries).
- Skipped: `make e2e-queues` (drives queues through workers and mgmt, not
  the tw CLI, whose parsing is what changed); `e2e-compat` / `e2e-wrangler`
  (binding resolution in a deploy is unchanged; the CLI resolver is new
  code covered by unit tests).
