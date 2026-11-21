Last update: 2026-11-17 PT. #110: the supervisor exits 1 when mgmt or the host stops on its own, so the service manager restarts it.

# fix-110-supervise-exit

Issue #110: `tw __supervise` returned nil (exit 0) after mgmt's listener
failed in the single-process layout, so neither systemd
(`Restart=on-failure`) nor launchd (`KeepAlive{SuccessfulExit=false}`)
restarted it.

## What changed

- `cmd/tw/supervise_exit.go` (new): `waitSingle` returns a `mgmt_failed`
  error on a listener failure and nil on a shutdown signal (a listener
  error racing a signal counts as the clean stop). `supStop` is the
  separate-host supervisor's stop latch: a signal stops with nil, a child
  that gives up (more than 10 exits in 5 min) stops with `mgmt_failed` /
  `host_failed`.
- `cmd/tw/up.go`: `superviseSingle` returns `waitSingle`'s error after
  closing both runtimes. `cmdSupervise` (two processes) used to log
  "giving up" and then keep running with a dead child until a signal; now
  the give-up stops the other child too and exits 1.
- Unit files unchanged: `Restart=on-failure` and `SuccessfulExit=false`
  already restart on a non-zero exit; `TestUnitFor` / `TestPlistFor` assert
  both.
- Docs: `docs/errors.md` (`mgmt_failed`, `host_failed`: also the
  supervisor's exit error), `docs/INSTALL.md` troubleshooting (exit code and
  where the log is). `changes/fix-110-supervise-exit.md`.

## Not done

- No in-process restart of a failed component in the single-process
  layout (the issue's optional item): the service manager restarts the
  whole supervisor instead. Without `--service` the supervisor still just
  stops (now with exit 1 and the reason in `supervisor.log`).
- The embedded host agent has no failure channel like mgmt's `Errs()`; a
  host failure that panics already exits non-zero.

## Tests

- `go test ./cmd/tw -run 'WaitSingle|SupStop|UnitFor|PlistFor' -count=1 -v`:
  all 5 pass (new: `TestWaitSingleListenerFailureExitsNonZero`,
  `TestWaitSingleSignalExitsZero`, `TestSupStop`; existing `TestUnitFor` /
  `TestPlistFor` assert `Restart=on-failure` and `SuccessfulExit=false`).
- `make lint test` after merging origin/master: pass (exit 0).
- Not run: a live kill of mgmt's listener under `systemctl --user` /
  launchd (needs a real tailnet node); the restart there follows from the
  exit code and the unit settings the tests assert.
