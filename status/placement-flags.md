# placement-flags (#525, addPlacementFlags half)

`tw deploy`, `tw promote` and `tw rollback` register --host, --replace,
--force-takeover, --allow-missing-grants, --no-wait and --wait through one
helper, `addPlacementFlags(f, verb)` in `cmd/tw/placementflags.go`, with one
help text per flag (deploy's fuller wording, verb-neutral; only
--allow-missing-grants names the verb), and `placementOpts.apply(body)` sets
host/replace/forceTakeover in the request. The helper also lists the flags
under Advanced in --help, once for all three.

Not here: the shared `waitReady()` loop (#525's other half) is PR #617 (outside
contributor). This branch keeps the `wait`, `noWait` and `allowMissing`
variable names in each command, so #617's loop code still compiles after
either lands first. The preflight `policy_unavailable` fallback stays per
command (not in scope of this half).

## Tests
- `go test ./cmd/tw -run 'TestPlacement|TestCLIReferenceUpToDate|TestCompletion|TestFlagGroups|TestCLIJSONContract|TestAgentEval'`: ok.
- `make cli-docs`: docs/cli.md updated (promote/rollback now show deploy's full flag texts).
- `make lint`: ok. `make test` (local): one failure, `TestLimitsDocCitations` (docs/limits.md cited `cmd/tw/app.go:-wait`); fixed by citing `cmd/tw/placementflags.go:-wait`, `go test ./docs` ok. Everything else passed.
- Merged with PR #617's head locally: clean merge, `go vet ./cmd/tw` and `TestPlacement*` ok.
