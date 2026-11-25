Last update: 2026-11-24. Docs-only fast path in CI and AGENTS.md.

## What changed

- `scripts/docs-only.sh [base]`: exit 0 when every changed file is `.md` and
  none is under `testdata/` or `e2e/`.
- `make lint-docs` (api-check, compat-check, changes-check, check-secrets) and
  `make test-docs` (root package, `./docs/...`, `./agent/...`,
  `./internal/catalog/...`, `./internal/compatdoc/...`, and the doc
  tests in `./cmd/tw`): the gates a Markdown-only change can actually fail.
- `ci.yml`: new `changes` job. On a docs-only `pull_request`, lint and test
  run the docs targets and the suites are skipped; `ci-ok` accepts `skipped`
  only in that case. Master pushes, merge_group and workflow_dispatch always
  run everything.
- AGENTS.md Definition of done, step 1: docs-only branches run
  `make lint-docs test-docs` locally instead of `make lint test`.

Why not skip CI outright: many `.md` files are embedded in `tw` (`docs/`,
`README.md`, `CHANGELOG.md`, `SECURITY.md`, agent skills) or checked by tests
(doc references, ADR index, CLI reference), and `docs/cli.md` and
`docs/contracts/mgmt-api.md` are generated.

## Tests

- `make lint-docs test-docs`: pass (~10 s locally, warm cache).
- `go test -v -run '...' ./cmd/tw/` with the test-docs pattern: 8 tests ran
  and passed (the pattern matches real tests).
- `make lint test`: pass.
- No extra suites apply (no mgmt, e2e or runtime changes).
- Not checked locally: the docs-only CI path itself. This PR isn't
  docs-only, so it takes the full path; the first `.md`-only PR exercises it.
