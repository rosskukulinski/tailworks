Last update: 2026-11-25 PT. Issue #528: one generated Environment section in docs/cli.md for the TW_* variables; tw upgrade honours TW_RELEASE_API.

# env-table

## What changed

- **`cmd/tw/envvars.go`**: the table of variables `tw` reads, plus
  `TW_HOME` (scripts only) and `CI`: what each does, the flag it is the
  environment form of, and whether `get.sh`/`install.sh` read it too.
  `cliReference` renders it as `## Environment` at the end of docs/cli.md
  (`make cli-docs`), with one sentence that the `tw up --service` unit
  passes none of them and that `TW_*`/`__TW*` names never reach a Worker.
  `TW_ALLOW_UNSIGNED` says "per invocation; never export it";
  `TW_RELEASE_SIGNERS` and `TW_RELEASE_API` say "tests and private
  builds/mirrors". Debugging/test knobs are a footer, not rows:
  `TW_KEEP_INSTANCES` (with the "secrets and VM disks stay on disk, never
  on a production host" warning; name kept) and `TW_TEST_LINK_NAME`.
- **TestCLIReferenceEnvVars** (`cmd/tw/envvars_test.go`): the TW_* names
  read by `Getenv`/`LookupEnv` in cmd/tw, internal/hostdrun and hostd
  (non-test, hostd/hostdtest excluded) equal the table's. Runs under
  `make test-docs` (name matches `CLIReference`).
- **TW_RELEASE_API**: `releaseAPI()` (updatecheck.go) is now used by
  `tw upgrade`'s `latestTag` too, and `get.sh` honours it; a mirror-named
  tag still goes through the signature check and the no-downgrade
  comparison, which run after the download whatever the source.
  `TestLatestTagReleaseAPI`.
- **TW_LINK_DISCOVER_NAME → TW_TEST_LINK_NAME** (link.go and its two tests;
  only in-repo tests set it).
- **install.sh** usage lists `--force` (help range `2,21p`).
- docs/INSTALL.md (What is in ~/.tailworks), docs/README.md and
  CONTRIBUTING.md point at the section instead of a second table.

## Tests

- `make lint`: ok (after merging origin/master).
- `make test-docs`: ok (includes TestCLIReferenceEnvVars, TestCLIReferenceUpToDate).
- `make install-test`: ok.
- `make cli-docs`: docs/cli.md regenerated.
- `go test -race ./cmd/tw ./internal/hostdrun`: ok (includes
  TestLatestTagReleaseAPI and the TW_TEST_LINK_NAME tests). The full
  `make test` was not run locally: the mbp runner's slots stayed busy,
  and the only Go package changed is cmd/tw. CI runs `make test`.
- `make changes-check`: ok, no fragment needed.

Merging origin/master brought `TW_INSTALL_FORCE` (get.sh) and install.sh's
`--force` usage line from #560. The table lists the first, and this branch
no longer edits install.sh.
