# release-job-setup-go

release.yml's `release` job runs `scripts/release-notes.sh`, which uses
`go run`. The job had no `actions/setup-go` step, so it ran the runner's
own Go (1.26.11) against a go.mod that needs 1.27.4, and failed with
`go.mod requires go >= 1.27.4`. That was the only failure left in the
v0.5.0 run (45432926893) after every build and test job passed. The job
now sets up Go from go.mod like the others.

The v0.5.0 draft was made by hand from that run's `release-dist` artifact
with the job's own commands, after checking `SHA256SUMS` and
`SHA256SUMS.sig` against the pinned key.

## Tests

- `make lint`: ok (macOS arm64, Go 1.27.4).
- Not run: the `release` job itself (it only runs on a tag, not on a
  rehearsal), and `make test` (the change is the workflow file only).
