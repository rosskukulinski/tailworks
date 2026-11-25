Last update: 2026-11-15 PT. CLI ergonomics (issue #46): completion, whoami, grouped help, update check, source-checkout hints.

Stacked on PR #49 (`benchmark-dx-infra-vs-cloudflare-workerd-vercel`), which
already merged master with #40 (ADR-041, `twd upgrade`).

## Built

- **`twd completion bash|zsh|fish`** (`cmd/twd/completion.go`). The script
  is generated from the `commands` table: subcommands from each usage line
  (`kv key put`, `d1 migrations apply`, `image public add`, `expose on|off`,
  `upgrade status|pause|resume|abort`), flags from the flag set each command
  and subcommand hands back for `--help` (probed in-process, no side
  effects), plus the `--flags` its usage names. Value-taking flags (never a
  bool flag anywhere) skip their value when finding the command path.
  zsh shows each command's `twd help` line; fish too. `--json` prints
  `{shell, script}`.
- **`twd whoami [--json]`** (`cmd/twd/whoami.go`). Identity (kind, actor,
  login, node, tags), linked or local, transport, mgmt endpoint, the default
  tenant (admins only: others can't list tenants) and `can`: `admin`,
  `approve`, `mcpTools`, plus the raw `twd.dev/cap/*` names in `caps`.
  `GET /v1/whoami` gained `caps`, `approver` and `mcpTools`
  (`mgmt/whoami_caps.go`, `api/openapi.yaml`, `make api`). Against an older
  mgmt, `approve` and `mcpTools` are `null`, not false.
- **Grouped `twd help`** (`cmd/twd/helpgroups.go`): Set up · Develop · Deploy
  · Data · Observe · Operate, one line of ≤ 60 characters per command.
  `twd help --all` keeps the old full listing; `TestCLIJSONContract` now
  parses that. A command in no group lands in "Other" and fails
  `TestHelpGroups`.
- **`twd version --check`** (`cmd/twd/updatecheck.go`): the latest GitHub
  release (same repo and `TWD_REPO` override as get.sh and `twd upgrade`),
  versions compared with `internal/version` (suffixes ignored, `dev` and
  suffixed builds aren't compared), prints `twd upgrade` (falls back to the
  get.sh line if that command is absent), and `daemons`: the running
  `twd up`'s `/v1/status` version, `restartNeeded`, and the restart command
  (the service's when one is installed). No second upgrade command: the
  install itself stays `twd upgrade` (#40).
- **Once-a-day notice** after a successful command: only on a terminal
  stderr, not with `--json`, `CI`, `TWD_NO_UPDATE_CHECK=1`, a dev build, or
  after `version`/`completion`/`mcp`. Cache in `<state>/update-check.json`
  (never creates the state dir); 2 s timeout; a failed check waits a day.
- **Source-checkout hints** (`cmd/twd/sourcehints.go`): a twd checkout is
  found above the binary or the working dir (go.mod module line +
  `js/packages/create-twd`). `twd doctor`'s bundler fixes and a
  `build_failed` whose output has `Could not resolve "@twd/…"` say
  `cd <root> && make deps`; installed twd keeps "reinstall twd".
- Docs: `docs/cli-json.md` (identity and versions), `docs/errors.md`
  (`update_check_failed`, `build_failed` fix), `docs/INSTALL.md`
  (completion, update check), the `twd` skill (run `twd whoami --json`
  first). Schema: `$defs/whoami`, `$defs/completion`, `version`'s
  `--check` fields. Fragment: `changes/cli-ergonomics-issue-46.md`.

## Tests

- New: `TestCompletionTree`, `TestCompletionScripts` (`bash -n`, `zsh -n`;
  `fish -n` when fish is installed: it wasn't here), `TestCompletionBash`
  (runs `_twd` for ten command lines), `TestWhoami` (real twd-mgmt behind
  its WhoIs gate: an admin and an approver with an MCP grant; `--json`
  against the schema), `TestWhoamiOldMgmt`, `TestHelpGroups`, `TestSemver`,
  `TestVersionCheck` (fake GitHub API), `TestUpdateNotice`,
  `TestSourceCheckoutHints`.
- `make lint`: exit 0 (api-check, compat-check, changes check, gofmt, vet
  for every tag set, check-secrets).
- `go test -count=1 ./cmd/twd/ ./mgmt/ ./mgmt/api/... ./schema/...
  ./docs/... ./agent/...`: all ok (cmd/twd 17 s, mgmt 39 s), including
  `TestCLIJSONContract` (whoami and completion added to `cliSurface`;
  completion succeeds hermetically and matches its schema) and `TestHelp`.
- Not run: the full `make test` (`deps`, `-race` across every package, JS
  tests) and the e2e suites; nothing in them is touched beyond the
  packages above.

## Gaps

- fish completion is syntax-checked only where fish is installed.
- The update notice fetches synchronously (≤ 2 s, once a day); no
  background refresh.
- `twd version --check` duplicates `twd upgrade`'s small `latestTag`
  lookup instead of sharing it, to keep `cmd/twd/upgrade.go` untouched; it
  also returns the release URL and has a test hook (`TWD_RELEASE_API`).
