Last update: 2026-11-15 15:55 PT. Issue #63 fixed: twd upgrade picks the state dir's install prefix, and the health gate skips deployments that were already broken.

# twd upgrade: install prefix and health gate (#63)

## Built

- **Prefix** (`cmd/twd/upgrade_prefix.go`). The homelab `twd upgrade` now
  replaces the install that owns `--state-dir`. It checks, in this order:
  the binary the service runs (the LaunchAgent plist's `ProgramArguments`
  or the systemd unit's `ExecStart`), the running supervisor's executable
  (`/proc/<pid>/exe`, or `ps -o comm=` on macOS), and the state dir itself
  when it's an install.sh prefix. If none of those applies, it uses the
  binary's own prefix as before. When the binary's prefix differs, a real
  run fails with `prefix_mismatch` and `--plan` shows a warning. The new
  `--prefix <dir>` flag overrides the choice.
- **Health gate** (`cmd/twd/upgrade_health.go`). Deployment statuses are
  saved just before twd stops. After the restart, the gate only waits on
  deployments that passed then (`active`, `stopped` or `superseded`). If
  that snapshot can't be read, every deployment counts, as before. Anything
  not active is listed in three places: a warning in `--plan`, a `!` line at
  the start of the run, and `notActiveBefore` in the result (also added to
  `schema/cli.schema.json`).
- `docs/upgrading.md`: covers which install gets upgraded, the new gate
  rule, and how to do the first upgrade from a release that has no
  `twd upgrade` (`--prefix ~/.twd`).
- Release-note fragment: `changes/upgrade-prefix-health-issue-63.md`.

## Tests

- `go test ./cmd/twd -run 'TestServiceBinary|TestResolvePrefix|TestPrefixOfBinary|TestGate|TestNotActive'`: ok.
- `make lint`: ok.
- `make test`: everything passed except two tests in packages this branch
  doesn't touch:
  - `TestCLISingleProcess` (cmd/twd): `twd audit` failed under the
    full-suite load. It passes on its own:
    `go test ./cmd/twd -run 'TestCLISingleProcess$'`.
  - `TestR2Streaming` (hostd) fails on its own too:
    `r2_test.go:401: gen: 0 map[raw:Get "http://100.64.0.3/gen?key=huge&size=1073741824": context deadline exceeded (Client.Timeout exceeded while awaiting headers)]`.
    The branch has no hostd diff.

## Gaps

- The microVM helper warning in the plan still names `exeDir()/twd`
  rather than `<prefix>/bin/twd`.
- No end-to-end test runs a real 0.1.0 → new upgrade from an extracted
  release. The resolution logic and the gate are unit-tested.
