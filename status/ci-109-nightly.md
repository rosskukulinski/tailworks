Last update: 2026-11-17 PT. Nightly workflow for the credential-free suites and test-pg on a Postgres service container (issue #109).

# ci-109-nightly

## What's built

- `.github/workflows/nightly.yml` (schedule 08:17 UTC + `workflow_dispatch`):
  - `suite`: a matrix job per Makefile target, `fail-fast: false`, each
    `make deps` then `make <target>`: e2e-coldid, e2e-headscale (runner's
    Docker), e2e-local, e2e-locks, e2e-observability, e2e-placement,
    e2e-queues, e2e-wrangler, templates-typecheck, test-node, test-sdk,
    test-vitest. 30 min timeout each, 45 for e2e-headscale. A matrix entry's
    `allow_failure: <issue URL>` makes it continue-on-error with a warning
    (none set today).
  - `test-pg`: `make test-pg` against a `postgres:17-alpine` service
    container. Also runs on PRs that touch `mgmt/**`, `scripts/test-pg.sh`,
    `go.mod`/`go.sum` or the workflow (the `suite` matrix is skipped on PRs).
  - `report`: on a scheduled run with any failure, comments on the open
    "nightly suites failing" issue, or opens one (label flaky-test).
- `scripts/test-pg.sh`: when `TW_TEST_POSTGRES_DSN` is already set it uses
  that database and starts no container; otherwise unchanged (OrbStack
  container on 127.0.0.1:55433). `TW_TEST_PG_LOG` moves the log
  (default `/tmp/tw-test-pg.log`).
- `e2e/vitest/vitest.config.ts`: `testTimeout: 30_000` per project. The first
  call into the mcp and refund-approval templates takes 3.6-4.6 s on an M-series
  Mac, so the default 5 s timed out 1-2 tests on most runs of `make test-vitest`.
- AGENTS.md: one paragraph under "Definition of done" pointing at the nightly.

ci.yml is untouched (another branch is adding jobs to it).

## Test results (local, macOS arm64, this branch)

| Target | Result | Time |
| --- | --- | --- |
| test-sdk | pass | 45 s |
| e2e-placement | pass | 44 s |
| e2e-locks | pass | 83 s |
| e2e-queues | pass | 33 s |
| e2e-observability | pass | 22 s |
| e2e-local | pass | 29 s |
| e2e-wrangler | pass | 16 s |
| e2e-coldid | pass | 160 s |
| test-node | pass | 36 s |
| templates-typecheck | pass | 45 s |
| e2e-headscale (OrbStack Docker) | pass | 94 s |
| test-vitest (master) | FAIL 2 of 2 runs: mcp / refund-approval "Test timed out in 5000ms" | 17 s |
| test-vitest (with the timeout fix) | pass, 3 of 3 runs | 12 s |
| test-pg (OrbStack container, unchanged path) | pass, 106 PASS lines | ~4 min |
| test-pg with `TW_TEST_POSTGRES_DSN` set to a hand-started postgres:17-alpine and `PG_CTX=nonexistent-ctx` (proves no Docker use) | pass, 106 PASS lines | ~4 min |

The workflow YAML was parsed with Ruby's YAML loader (which caught a
`: ` inside a plain scalar). actionlint isn't installed here and wasn't run.

## Gaps / next steps

- Issue #109's other items are not done here: moving the fast suites
  (test-vitest, templates-typecheck) into ci.yml, and making compat-check
  mark COMPAT rows whose only evidence is a nightly suite. Tracked in #163.
