Last update: 2026-11-24 09:00 PT. CI Go caches pruned to what each job uses; module cache dropped.

Follows `status/ci-go-build-cache.md` (the per-job rolling caches).

## Findings (ci.yml runs on 2026-11-24)

Restoring the Go cache was the biggest fixed cost in `lint` and `test`, and
most of that time was spent unpacking it, not downloading it:

| job | archive | on disk | restore | used by a run |
| --- | --- | --- | --- | --- |
| lint | 5.5 GB | 29 GB | 117-210 s | 4.9 GB build + 0.1 GB staticcheck (0.95 GB compressed) |
| test | 3.0 GB | 15 GB | 107-118 s (25 s download, 82 s unpack) | 2.9 GB (0.57 GB compressed) |
| suite | 0.4 GB | | 5-9 s | |

Master pushes also spent 40-100 s per job saving the cache, and lint + test
alone came to about 8.5 GB of the repo's 10 GB Actions cache budget, so
saving a new lint cache before the old one was pruned pushed the repo over
the limit.

Cause: the rolling cache restores and re-saves the whole directory, and both
Go and staticcheck trim only entries unused for 5 days. So the cache carried
5 days' worth of master builds.

Is the cache worth having? A one-off experiment workflow (run 45435944973,
temporary branch, since deleted) on the same commit measured:

| | cold (no cache) | warm (master cache) |
| --- | --- | --- |
| `make lint` | 9 m 34 s | 16 s |
| `make test` | 12 m 08 s | 8 m 42 s |
| `go mod download` (both modules) | 5 s | |

- lint: yes, by far (vet across ~30 build-tag/GOOS sets, plus staticcheck).
- test: yes, about 3.5 min of -race compilation. `make test` uses `-count=1`,
  so test results never come from the cache; only compilation is saved.
- module cache: no. A cold download takes 5 s, less than unpacking it.

## Change

- `scripts/ci-go-cache.sh mark` after the restore and `prune` before the
  save, only on runs that save (master pushes, workflow_dispatch). `mark`
  backdates every cache file 2 h. Go and staticcheck bump the mtime of any
  entry they use that is more than 1 h old. `prune` then deletes every
  entry not newer than the mark. The backdate is 2 h, not more, because both
  tools trim entries more than 5 days old once their `trim.txt` is a day
  old. A deeper backdate would be trimmed by the job's first go command.
- Pruning runs only after a green run, so a failed master run saves the
  cache unpruned (as before) instead of a partial one.
- `~/go/pkg/mod` dropped from `GO_CACHE_PATHS`.

Expected: test restore ~100 s -> ~15 s (on the critical path), lint restore
2-3.5 min -> ~20 s, master saves well under a minute, and the total cache
around 2 GB. The first master push after merge still restores the old large
cache. It saves a pruned one, and every run after that uses the pruned one.

## Tests

- Linux (golang:1.27 container): build two projects into one GOCACHE, set
  `trim.txt` a week old, `mark`, build one project, `prune`. The trim didn't
  wipe the backdated cache. The used project rebuilt with 0 recompiles and
  the unused one's 10 packages were gone. `trim.txt`/README were kept, and
  `mark` with no cache dir does nothing.
- The workflow YAML parses. I didn't run `make lint test`: no Go, Make or JS
  changed. actionlint and shellcheck aren't installed here.

## Not done / next

- Docs-only PRs still restore the full lint and test caches to run
  `lint-docs` / `test-docs`. Once the caches are pruned that costs ~15 s.
- With test sharding (another workstream), every shard restores the test
  cache. Keep one saver, or prune per shard into separate keys.
