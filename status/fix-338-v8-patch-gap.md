Last update: 2026-11-24 PT. V8 patch gap (#338): the V8 feed is named and watched, `tw doctor` ages the pin, the update notice marks security releases, the V8 sandbox status is recorded.

# Issue #338: V8 patch gap

Branch `fix-338-v8-patch-gap`. The release process waited for workerd
advisories, but V8 fixes arrive as V8 rolls inside ordinary workerd
releases; the public signal is Chrome's stable-channel release notes.

## What's built

1. **RELEASING.md names the V8 feed and a target** ("Upstream advisories",
   new **V8** paragraph and the workerd row). Chrome stable release notes
   (Chrome Releases blog; chromiumdash for the current version), V8
   `MAJOR.MINOR` ↔ Chrome `MAJOR*10+MINOR`, which V8 a release carries and
   how to read it (`make v8-check`), the nightly job as what starts the
   clock, and the target: a patch release whose workerd carries the fixed V8
   within 7 days of the Chrome stable release that names a High or Critical
   V8 CVE.
2. **Automatic issue.** `internal/v8check` (library + tests),
   `internal/v8check/cmd` (the program), `scripts/v8-check.sh`, `make
   v8-check` (own `.PHONY`), and a `v8-check` job in
   `.github/workflows/nightly.yml` (schedule and workflow_dispatch; same
   SHA-pinned actions as the other jobs; `issues: write`). The job opens or
   updates one `security` issue, "workerd pin is behind Chrome stable's V8",
   with both versions and the Chrome release-notes link, and closes it when
   the pin catches up. The job itself stays green; the issue is the signal.
3. **Operator.** (a) `tw doctor` has a new `workerd_age` check
   (`cmd/tw/doctor_workerdage.go`, group Install): the pin's release date
   and age, `warn` past 30 days with a fix. It is a separate check rather
   than a changed `workerd` status so the calendar can't flip the existing
   check's tests. (b) The `security` fragment kind already existed and
   `scripts/release-notes.sh` renders a `## Security` section from it into
   the GitHub release body, which `/releases/latest` returns. `tw` now reads
   that body (`catalog.HasSecurityNotes`): `tw version --check` prints "—
   security release, upgrade soon" and `--json` has `securityRelease`
   (schema updated, `cli-json` fragment kind); the once-a-day notice after
   commands such as `tw doctor` and `tw status` says the same. No
   release-pipeline change was needed.
4. **V8 sandbox status** in `docs/security-model.md` ("The V8 sandbox in the
   shipped workerd"): not enabled, on all three platform builds, with the
   evidence and how to recheck.
5. `security/LEDGER.md`: `H-patch-gap` row (Medium, **fixed** (process)) and
   a run-log line.

## Findings

- `process.versions.v8` is `""` under workerd 1.20261121.1 with
  `nodejs_compat` (macOS arm64 and Linux amd64), so a Worker can't report
  the V8 version. `workerd --version` prints only a date. The version is
  read out of the binary instead: exactly one standalone
  `MAJOR.MINOR.BUILD.PATCH` string with a two-digit major, `15.8.62.5`, on
  darwin-arm64, linux-64 and linux-arm64. The scanner fails rather than
  guesses when there are zero or several candidates.
- Chrome stable on 2026-11-23 was 158.0.8127.88 (V8 15.8): the pin is
  current, `make v8-check` prints `ok`. A fixture with Chrome 160 makes it
  exit 10 and write the issue body.
- V8 sandbox: two strings that look like evidence are not. `sandbox_testing`
  and `sandbox_fuzzing` are defined (read-only) in V8 15.8 builds without
  the sandbox too (`src/flags/flag-definitions.h`), and `v8_enable_sandbox:
  0` is workerd's copy of Node's `process.config`
  (`src/node/internal/internal_process.ts`). The real evidence: V8's Bazel
  `bazel/defs.bzl` hardcodes `sandbox = false` and `BUILD.bazel` lists
  `v8_enable_sandbox` as unsupported; workerd's `jsg.c++` says it isn't
  built with it; and at run time the pinned workerd serving one Worker
  reserves a 4 GiB region (pointer-compression cage) and no 1 TiB sandbox:
  `vmmap` on macOS arm64, `/proc/<pid>/maps` on the amd64 Linux VM
  (`ssh twd-amd64`) with the `workerd-linux-64` npm binary.

## Tests

Commands run from this worktree (results filled in below):

- `go test -count=1 ./internal/v8check/... ./internal/catalog/...`: ok.
- `go test -count=1 -run 'TestDoctor|TestWorkerdAgeCheck|TestSecurityReleaseNotice|TestUpdateNotice|TestVersionCheck|TestCLIReferenceUpToDate|TestVersionReportsUpdateCheck|TestNoUpdateCheckAfterLocalCommands' ./cmd/tw`: ok
  (TestDoctor needs `make js-build` first; without it the bundler check
  fails for every branch).
- `make v8-check`: `ok: workerd 1.20261121.1 carries V8 15.8.62.5 (Chrome
  158); Chrome stable is 158.0.8127.88 (2026-11-23)`, exit 0.
  `V8_CHECK_CHROME=<fixture with Chrome 160> sh scripts/v8-check.sh`:
  `BEHIND: … V8 16.0`, exit 10. The `V8_CHECK_ISSUE=1` path (gh issue
  create/edit/close) was not run here: it needs the repository token; the
  first scheduled nightly, or `gh workflow run nightly.yml`, exercises it.
- `go run ./internal/v8check/cmd -check-pin=false -workerd <npm linux-64 and
  linux-arm64 binaries>`: both `15.8.62.5`, ok.
- `make test-docs`: ok.
- `make lint`: ok, before and after merging `origin/master` (b621c343);
  check-changes: `needs: cli-json; found in changes/fix-338-v8-patch-gap.md`.
- `make test` (offloaded: `sh ~/.claude/scratch/mbp-test.sh test`, pre-merge
  tree): 57 packages ok; 3 timing failures on the shared runner, none in
  code this branch touches: `mgmt.TestAPIRateLimitBehindTailnetHandler`
  (200, want 429), `mgmt.TestWorkloadReplicas` (hosts "not live" at
  deploy), `hostd.TestRealMgmtStores/Queues` (no retry delivery within
  20s). All three pass locally with `go test -race -count=1 -run … ./mgmt`
  and `… ./hostd` on the merged tree.
- Not run: `make e2e-upgrade` (`make changes-check` doesn't call the
  `cli-json` addition upgrade-relevant beyond the fragment; the change adds
  one optional JSON field), suites needing credentials.

## Gaps and next steps

- The milestone comparison doesn't see a V8 patch fix inside the same
  milestone (Chrome 158.0.8127.36 → .88 both carry V8 15.8). Comparing the
  V8 branch's BUILD.PATCH would need the V8 version Chrome ships per
  release, which chromiumdash's `fetch_releases` gives only as a hash.
- The chain is still Chrome release → issue → human triage → patch release
  → operator upgrade. Nothing auto-bumps the pin or auto-releases (option 2
  of the issue).
- mgmt doesn't list hosts that are behind a security release (the issue's
  option 3, last clause).
- The nightly job's issue path is untested until the first scheduled run.
