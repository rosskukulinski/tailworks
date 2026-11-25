Last update: 2026-11-24 PT. `tw help` regrouped (#435, #436, #446); `--arch` only on `tw image public add`.

# simplify-help-groups

Part of the interface simplification audit (#434).

## What changed

- #436: new "Coding agents" group, right after "Develop", for `mcp` and
  `agent`. `docs` stays in Develop: it is for people too.
- #435: new "Scale out" group for `tenant` and `host` (`tw up` creates the
  operator tenant and runs the host itself), and "Advanced" (last) for
  `custody` and `release`, with sharper short lines. `host`'s line still says
  "set up". notify, telemetry and completion were not moved; `commands` order
  is unchanged.
- #446: `image` moves from Deploy to Operate (not hidden: error texts in
  `internal/registry/pin.go` and `imagebuild.go` point at `tw image push`).
  `--arch` is registered only on `tw image public add`, so `ls` and `rm`
  reject it as an unknown flag (`TestImagePublicArchOnlyOnAdd`).
- The three new group constants sit in their own const block in
  `cmd/tw/helpgroups.go`, so the six existing lines keep their alignment.
- `make cli-docs` regenerated `docs/cli.md`.

## Tests

- `go test ./cmd/tw -run 'TestImagePublicArchOnlyOnAdd|TestHelpGroups|TestCompletion|TestCLIReferenceUpToDate'`: ok
  (before and after merging `origin/master`)
- `make changes-check`: ok (no upgrade-relevant paths changed; no fragment needed)
- `make lint`: ok (before and after merging `origin/master`)
- `make test`: `cmd/tw` ok and every other package ok, except four
  timeouts in `mgmt` and `hostd` (code this branch does not touch) while the
  machine's load average was about 180 from parallel agents:
  `TestTenantAdminScope` (host heartbeat not seen within 10 s),
  `TestLockTemplate/ClientWithLockRenewsAndFences`, `TestWorkersKV/DeleteAndLimits`,
  `TestR2Streaming` (1 GiB client timeout). Rerun alone:
  `TestTenantAdminScope` ok; `TestR2Streaming` timed out again under the same
  load (it is load-sensitive; see `status/fix-linux-ci-tests-issue-75.md`).
  CI is the check for these.
- No extra suite from the AGENTS.md table applies (CLI help grouping and one
  flag registration only).
