Last update: 2026-11-23 PT. `tw up --worker-internet=off|direct` (#295 item 5).

# tw up --worker-internet (#295, item 5)

Follows `status/feat-161-worker-internet-egress.md` (ADR-049), which had
only `tw-hostd -worker-internet=off` for turning off Workers' internet egress
for a whole host.

## What's built

- `cmd/tw/up_workerinternet.go` (new): the `--worker-internet` flag
  (`direct`, the default, or `off`), saved in `tw.json` as `workerInternet`
  (empty, which is state written before this, means `direct`). It reaches
  the host in both layouts: `tw-hostd -worker-internet off` with
  `--separate-host`, and `hostdrun.Options.WorkerInternet` in single
  process. Also here: the `tw status` line and the `tw doctor` check.
- Small edits to shared files: one field each in `Config` (state.go),
  `statusResult` and `hostd.StatusView`; one flag, one save, one hostd-args
  append and one single-process call in up.go; one `keep` in up_saved.go
  (so a restart and `tw upgrade`'s `tw up --yes` keep it); one `run` in
  doctor.go and its entry in doctor_render.go.
- `hostd.StatusView.WorkerInternet` (`direct`/`off`, `hostd/internet.go`):
  the mode the running host actually has. `tw status` shows that value (or
  tw.json's when the host doesn't say). `tw doctor`'s `worker_internet`
  check warns when the running host differs from tw.json (restart needed).
- `tw status --json` has a new `workerInternet` field
  (`schema/cli.schema.json`), so there is a `changes/` fragment with
  `upgrade: [cli-json]`.
- Docs: `docs/cli.md` (regenerated), errors.md
  (`internet_egress_unavailable`), security-model.md,
  tailworks-jsonc.md. netd's refusal detail now names
  `tw up --worker-internet=off` as well as tw-hostd's flag.

## Tests

- New: `TestUpWorkerInternet` (default, validation, saved, kept by a later
  tw up, a flag given again wins, hostd args, single-process option),
  `TestWorkerInternetDetail`, `TestDoctorWorkerInternet`,
  `hostd:TestWorkerInternetMode`. `TestDoctor` and
  `TestDoctorChecksHaveGroups` now include `worker_internet`.
- `e2e/twupgrade`: the first `tw up` passes `--worker-internet=off`.
  tw.json and `tw status --json` (host up, mode reported by the host) must
  still say `off` before the upgrades, after each rolled-back N+1, after the
  good upgrade, and after a plain `tw down` / `tw up --yes`.

Commands run:

- `make lint` (local): PASS, `check-changes: ok (needs: cli-json; found in changes/fix-295-worker-internet.md)`.
- `make cli-docs` (local): regenerated `docs/cli.md`.
- `sh ~/.claude/scratch/mbp-test.sh -k test e2e-tw-upgrade e2e-upgrade` (mbp):
  PASS. `make test` all packages ok; `TestTwUpgradeSingleMachine` PASS (89.5 s),
  `TestUpDownBudget` PASS; `TestUpgradePreviousToCandidate` PASS. The first
  mbp run failed `TestDoctor` (it didn't list the new check yet), and that's fixed.

## Gaps

- A remote host added with `tw host add` still uses its own
  `tw-hostd -worker-internet` flag. `tw doctor` on a linked machine skips
  the check and points to the host.
- A new setting takes effect when the host starts (`tw down`, then
  `tw up --worker-internet=…`). If tw.json and the running host ever
  differ, `tw doctor` warns and `tw status` marks the line `!`.
