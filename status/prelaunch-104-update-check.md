Last update: 2026-11-17 00:10 PT. Issue #104: no update check after tw init / tw dev; disclosure in README and the security model.

## What changed

- `cmd/tw/updatecheck.go`: `dev` and `init` join `noUpdateNotice`, so the
  once-a-day check (`GET api.github.com/repos/<repo>/releases/latest`) never
  runs after them. The env/build gate is split out as `updateCheckOn()`;
  the terminal test is a `stderrIsTerminal` var so tests can exercise the
  real path.
- `tw version --json` (and `--check --json`) adds `updateCheck` (bool):
  whether this tw runs the check. Schema `$defs/version` gains the property
  (not required).
- Docs: README "What tw sends" (one click to the details);
  `docs/security-model.md` new self-contained section "Telemetry and the
  update check" (what is sent, when, when never, how to turn it off; no
  other telemetry; `tw telemetry` is the user's own OTLP export);
  `docs/INSTALL.md` and `docs/cli-json.md` updated.
- `changes/prelaunch-104-update-check.md` (kind fix; cli-json,
  config-default).

## Tests

- `TestNoUpdateCheckAfterLocalCommands`: points `TW_RELEASE_API` at a
  counting httptest server, pretends stderr is a terminal and runs as
  release 0.2.0 with the check due; runs `tw init` and `tw dev --once`
  through `run()` (dev only when workerd and node are present) and asserts
  zero requests and no `update-check.json`; control: the same setup after
  `ls` makes exactly one request and prints the notice.
- `TestVersionReportsUpdateCheck`: `updateCheck` true for a release build,
  false with `TW_NO_UPDATE_CHECK=1`, for `0.2.0-dev` and `dev`.
- `go test ./cmd/tw -run 'TestNoUpdateCheckAfterLocalCommands|TestVersionReportsUpdateCheck|TestUpdateNotice|TestVersionCheck|TestSemver'`: pass (after `make deps`, tw dev --once ran in-process).
- `make lint test`: lint (incl. changes-check, gofmt, api-check) passes;
  all test packages pass, including `cmd/tw` and `schema`, except
  `netd` `TestEgressWaitsForNewPeerPath` (2.49 s against a 2 s bound),
  a timing test unrelated to this branch, failing locally under load
  average 35-44 from parallel agent runs. Left to CI.

## Not done

- No opt-in-on-first-run prompt (the issue's alternative); the check stays
  on by default for commands that already talk to mgmt, with the opt-out
  disclosed.
