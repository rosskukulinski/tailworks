Last update: 2026-11-23 18:00 PT. Per-job rolling Go caches in ci.yml; not yet measured on CI.

# CI Go build caching

## Problem

`ci.yml` used setup-go's built-in cache. That gives one key per `go.sum`
shared by all five jobs. The first job to finish saved it (often a small
suite job), and every later run got a primary-key hit and never saved
again. The cache was a frozen, mostly irrelevant snapshot:

- lint, 10 min (master run 2026-11-24 01:13 UTC): `go vet ./...` 1.5 min,
  staticcheck 4 min (its `~/.cache/staticcheck` was never cached), ~30
  tagged `go vet` runs 3.5 min.
- test, 11.5 min: `go test -race` builds everything with -race from scratch.
- PR runs also saved PR-scoped setup-go entries (one of 1.1 GB for #209)
  that no other ref can read. The repo was at 10.6 GB of the 10 GB Actions
  cache budget, so entries were being evicted.

## Change

- setup-go `cache: false` in ci.yml (nightly/release unchanged).
- Each of lint, test, suite restores `go-ci-<job>-<os>-<hash of go.mod/go.sum
  and tools/>-<sha>` with prefix fallbacks: module cache, build cache,
  staticcheck cache. The three suite matrix jobs share one; e2e-local saves it.
- Only `push` (master) and `workflow_dispatch` save. PR and merge-queue runs
  restore master's newest entry and never write.
- `go-cache-prune` (push/dispatch only, its own job so `actions: write`
  never reaches jobs running branch code) keeps the newest `go-ci-<job>-`
  entry per job on the ref and deletes the rest.

## Expected effect

Lint should drop the most (vet and staticcheck results are cached by
content). Test keeps `-count=1`, so tests still run; only the -race compile
is saved, and cmd/tw's ~9-10 min of test runtime stays the floor.

## Tests

- YAML parses; job/step layout checked with Ruby's YAML loader.
- `make changes-check`: ok (no upgrade-relevant paths).
- Not run: actionlint (not installed), and a real CI run. The first master
  push after merge saves the caches; the PR's own run is cold.
