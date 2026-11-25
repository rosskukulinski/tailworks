Last update: 2026-11-24. Flag groups and hidden flags in `--help` (#463), tw dev usage line (#445).

## What's built

- `cmd/tw/helpflags.go`: `f.group(title, names...)` and
  `f.hidden(why, names...)` on the `flags` type; `writeFlagHelp` prints
  `flags:` (ungrouped, everyday flags; the anchor clidoc.go relies on),
  then each group, then `also accepted, <why>: --a --b` for hidden flags,
  then one `global:` line (`--state-dir`, `--json`, and `--tenant` when it
  is tenantFlag's). Rendering goes through a scratch FlagSet, so flag
  lines are the stdlib's. Hidden flags stay in the FlagSet: parsing,
  completion, saved tw up flags and the docs checks are unchanged.
- `yesFlagBefore(f, verb)`: every `--yes` (up, deploy, tenant add,
  policy, agent, upgrade, and the destructive `yesFlag`) reads "skip the
  confirmation before <verb>; needed without a terminal and with --json".
  `yesFlag(f)` keeps its signature so the data commands' files are untouched.
- Applied: tw up (groups in `up_help.go`; `up_store.go`, `up_retention.go`,
  `up_workerinternet.go` add their own), deploy / promote / rollback /
  dev / link / ls (Advanced), host setup (hidden `--firecracker`,
  `--jailer`), logs (usage reordered, `--host` / `--outcome` text).
- Usage lines: up, dev (#445), deploy, promote, rollback, logs rewritten;
  `[--tenant <tailnet>]` dropped from the non-data commands' lines (kept on
  deploy as the issue's proposed line has it).
- login.go's login_timeout fix text names `tw link --tsnet
  --tsnet-authkey-file`.

## Judgment calls

- tw dev: `--once` and `--test-scheduled` are in #445's usage line, so they
  stay in the everyday list; #463's Advanced group for dev is
  `--console-port --inspector-port --workerd -v`.
- Data commands (secret, kv, queues, d1, r2) keep `[--tenant <tailnet>]`
  in their usage lines: other branches are restructuring them (#461,
  #462, #458). Their --help already moves `--tenant` to the global line.
- tw up's flags the issue didn't place: `--heartbeat`, `--kill-tombstone`,
  `--device-delete-delay` under Tuning, `--push-port` under Layout.
- `tw link --tsnet-authkey-file` / `--tsnet-tags` said "(tests)"; reworded
  (automation) since they're advanced, not hidden.

## Tests

- `go test ./cmd/tw ./docs`: ok (new `TestFlagGroupsResolve`,
  `TestFlagHelpLayout`, `TestYesFlagWording`).
- `make cli-docs`: docs/cli.md regenerated (about 130 lines shorter).
- `make lint`: ok (before and after merging origin/master).
- `make test`: everything passed except `TestCLIOAuth` (cmd/tw) and
  `TestR2Binding` (hostd), on a machine at load ~200; both pass on their
  own (`go test ./cmd/tw -run '^TestCLIOAuth$'`, `go test ./hostd -run
  '^TestR2Binding$'`). Neither reads help text.
- After merging origin/master: `make cli-docs`, `go test ./cmd/tw ./docs
  ./agent`: ok.
- No extra suites apply (help text and flag registration only).
