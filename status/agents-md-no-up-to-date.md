Last update: 2026-11-23 18:45 PT. AGENTS.md matches the ruleset: branches no longer need to be up to date.

# AGENTS.md: no "up to date" requirement

On 2026-11-23 the `master: PR + CI` ruleset stopped requiring branches to be
up to date with master (`ci-ok` is still required), to cut merge cycles after
CI got per-job Go caches (status/ci-go-build-cache.md). AGENTS.md's
"Definition of done" still told agents to re-merge master and rerun CI
whenever another PR landed first. It now says that's not needed unless the
PRs touch the same code, that the push-to-master CI run is the backstop, and
that a red master is fixed before anything else lands.

## Tests

- `make changes-check`: ok. Docs-only change; `make lint test` not run.
