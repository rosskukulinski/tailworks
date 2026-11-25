Last update: 2026-11-24 17:40 PT. `make e2e-multimgmt` added and wired into nightly.

## What

The multi-replica tw-mgmt chaos suite (`e2e/multimgmt`, LOOP.md S1–S12 and
the H-regressions, invariants I1–I11) had no Makefile target and ran
nowhere, so it could rot without anyone noticing. This branch:

- adds `make e2e-multimgmt`. It uses the docker context `PG_CTX`, takes
  `TW_MM_SEED` (default 1) and `MM_RUN` to rerun one scenario, and has a
  45 min timeout;
- adds it to the `nightly.yml` suite matrix (timeout 60). It runs with
  `PG_CTX=default` (the runner's Docker) and `TW_MM_SEED=<run number>`, so
  each night gets a new fault schedule. It uploads `e2e/multimgmt/results/`
  on failure;
- adds an AGENTS.md "Also run" row for mgmt code that every replica runs.

Follow-up for the data plane (Queues, DO/locks fencing with a real hostd, D1
across moves) and multimgmt ledger item 3: see the issue linked in the PR.

## Tests

- Full suite, `go test -tags e2emultimgmt -count=1 -v ./e2e/multimgmt/`
  (seed 1, OrbStack, macOS): **all 24 pass**, 1216.7 s (~20 min).
- `make e2e-multimgmt MM_RUN='TestMM_H01|TestMM_S12' TW_MM_SEED=7`: pass.
  The seed and `-run` pass through.
- `make lint`: pass. `make changes-check`: ok (no upgrade-relevant paths).
- `make test`: one failure, `netd:TestKeepEarlyAnswerNoAnswer` (POST took
  12 s under full-suite load). It passes `-count=3` in isolation. It's a
  timing flake, unrelated: this branch has no Go changes.
- Not run: the nightly job itself on GitHub's runner. The first scheduled
  run (or `gh workflow run nightly.yml`) is its first Linux run.
