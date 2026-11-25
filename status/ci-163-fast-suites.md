Last update: 2026-11-21 13:45 PT. Fast suites in ci.yml; compat-check flags nightly-only rows (issue #163).

## What changed

- `.github/workflows/ci.yml`: new `suite` matrix job, one leg each for
  `e2e-local`, `templates-typecheck` and `test-vitest`. The legs run in
  parallel with `lint` and `test` (`make deps`, then the target). `ci-ok`
  keeps its name and is still the single required check; it now needs
  `lint`, `suite` and `test`. A matrix job's result is `success` only when
  every leg passes, so one failing leg fails `ci-ok`.
- `.github/workflows/nightly.yml`: the three targets leave the matrix, since
  every PR and every push to master now runs them.
- `internal/compatdoc/nightly.go`: compat-check fails on a ✅/⚠️ COMPAT.md
  row when every test it cites runs only in a nightly or by-hand suite,
  unless its Tests cell says "nightly". A test counts as per-PR when it is
  under `make test` (outside `e2e/`, in a file with no build tag other than
  an OS or arch tag, or a JS test file) or under `e2e/local` or `e2e/vitest`.
  The header of docs/COMPAT.md documents the rule.
- docs/COMPAT.md: `agents-sdk`, `mcp` and `vercel-ai` now say
  `(nightly: make test-sdk[, make test-node])`. These were exactly the rows
  the new check flagged.

The govulncheck/staticcheck prerequisite had already landed (#166, #186).
It changed the Makefile's `lint` target, not ci.yml, so this change didn't
conflict with it.

## Tests

- `go test ./internal/compatdoc/`: ok. `TestNightlyOnlyRowMustSaySo` has 11
  cases. Before the docs edit, compatcheck failed on the 3 rows.
- `make lint`: ok.
- `make test-vitest`: ok, 9 s. `make e2e-local`: ok, 22 s.
  `make templates-typecheck`: ok, 29 s. All local runs on macOS arm64.
- `make test` (mbp runner): ok.
- The PR's own CI run shows whether the new jobs work on ubuntu-24.04.
