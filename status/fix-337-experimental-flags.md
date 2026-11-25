Last update: 2026-11-24. Experimental workerd compatibility flags are refused at deploy unless the host allows them (#337, ledger H-compat-flags).

# fix-337-experimental-flags: tenant compatibility flags vs. `--experimental`

## What

- The fresh-eyes pass of 2026-11-23 (#337) found that every workerd
  tailworks starts runs with `--experimental` (the platform worker needs it
  for the worker loader and facets), and that a tenant's
  `compatibility_flags` reached it filtered only by `^[a-z0-9_]+$`. So
  `unsafe_module` (`workerd:unsafe`, `workerd:unsafe-eval`), `rtti_api`,
  `precise_timers` and every experimental flag a workerd bump brings were
  tenant-selectable. Everything stays in the tenant's own process; the
  concern is the unreviewed door.
- New package `internal/compatflags`: the pinned workerd's experimental
  flags (`Experimental`, 45 names from upstream's
  `src/workerd/io/compatibility-date.capnp` at `v1.20261121.1`, one per
  line), `Filter`, `InBundle`, and `Probe`, which asks a workerd binary
  directly: `workerd test` over a throwaway config carrying the flags,
  without `--experimental`, exits in ~20 ms with one line per experimental
  flag ("The compatibility flag X is experimental …"). Cached per (binary,
  flag set).
- `hostd/gen/compatflags.go`: `Generate` refuses the app when its flags
  include one the list names or one the workerd at `Options.WorkerdBin`
  refuses, with the typed `*gen.ExperimentalFlagError` (code
  `experimental_flag`), unless `Options.AllowExperimentalFlags`. One call
  in `Generate`, right after the flag-name check, so the tenant service and
  the facets' Dynamic Workers (both from `tenantFlags(app.CompatFlags)`)
  are covered by one check.
- hostd: `Config.AllowExperimentalFlags` (tw-hostd
  `-allow-experimental-flags`, `tw up --allow-experimental-flags`, saved in
  the `tw up` config). The host passes it and its workerd path to `gen`,
  and advertises the new `experimental-flags` feature
  (`internal/features.ExperimentalFlags`) when set; `features.Required`
  asks for it when the bundle lists an experimental flag, so placement
  pins such a deployment to a host that allows them.
- mgmt (`mgmt/compatflags.go`): `checkCompatFlags` refuses the deploy and
  its dry run (`tw deploy`'s preflight is `POST /v1/deployments?dryRun=true`)
  with `403 experimental_flag` when the bundle lists experimental flags and
  no registered host advertises the feature. mgmt has no workerd, so it
  uses the list; the host is the backstop for flags the list doesn't name
  yet.
- Docs: `docs/errors.md#experimental_flag`, `docs/COMPAT.md` (Workers
  platform config: the policy, which flags a tenant may set, the operator
  switch; anchor `experimental-flags`), `docs/security-model.md`
  (Experimental compatibility flags), `docs/cli.md` (`tw up` flag), the
  API spec's error table and enum (`make api`), `changes/` fragment
  (security; config-default, feature-string, protocol), ledger row
  `H-compat-flags` and run-log line.

## Which option, and why

Both of the issue's options 1 and 2, because neither alone covers every
place:

- **Ask workerd (option 1)** is cheap and follows upstream across bumps:
  `workerd test <cfg>` without `--experimental` exits in ~20 ms and names
  every experimental flag; `workerd compile` does not check flags, and
  `serve` would need a socket. hostd has the pinned binary, so it asks at
  deploy (cached per flag set). mgmt and the CLI have none.
- **A list (option 2)** is what mgmt uses for the early, typed refusal
  before any host is picked. The npm `workerd` package ships only
  `workerd.capnp`, not `compatibility-date.capnp`, so the list is a copy,
  pinned two ways: `TestExperimentalMatchesUpstream` fetches the capnp of
  `version.WorkerdPin` from GitHub (skips offline or with `-short`;
  `TW_WORKERD_CAPNP` points at a local copy) and diffs the `$experimental`
  fields against the list; `TestExperimentalRefusedByWorkerd` checks the
  pinned binary refuses exactly the listed flags. A bump that adds an
  experimental flag fails `make test` until the list is updated, and the
  host refuses the new flag from the probe in the meantime.

## Measured before / after (pinned workerd 1.20261121.1)

Before (master, the test's first version with the fix absent):

```
=== RUN   TestBeforeExperimentalFlags
    zz_before_test.go:42: Generate accepted compatibility_flags [nodejs_compat unsafe_module rtti_api precise_timers] (want a refusal: experimental_flag)
    zz_before_test.go:47: Worker: GET / = 200 map[unsafe:true]
    zz_before_test.go:49: Worker: tenant code imported workerd:unsafe
    zz_before_test.go:47: Facet: GET / = 500 map[raw:Internal Server Error]
    gen_test.go:216: workerd log:
        workerd/server/server/listen/mod.rs:393: error: Uncaught exception: … The compatibility flag rtti_api is experimental and may break or be removed in a future version of workerd. To use this flag, you must pass --experimental on the command line.
--- FAIL: TestBeforeExperimentalFlags (0.23s)
```

Finding: the facet's Dynamic Worker (`env.LOADER.get`) refuses the
experimental flags on its own; the worker loader does not inherit
`--experimental`. So on master the door was open in the Worker (and
plain-DO objects, which run in the tenant service), and a facet object
with such flags crashed. The issue's "and to every facet's Dynamic
Worker" is the config path, not an exercised import.

After:

```
=== RUN   TestExperimentalFlagsRefused
--- PASS: TestExperimentalFlagsRefused (0.42s)
    --- PASS: TestExperimentalFlagsRefused/RefusedByDefault/KnownList (0.00s)
    --- PASS: TestExperimentalFlagsRefused/RefusedByDefault/AskWorkerd (0.02s)
    --- PASS: TestExperimentalFlagsRefused/StableFlagsPass (0.36s)
    --- PASS: TestExperimentalFlagsRefused/AllowedWithOperatorSwitch (0.03s)
```

`RefusedByDefault` fails on master (Generate succeeds);
`AllowedWithOperatorSwitch` shows `-allow-experimental-flags` restores the
`workerd:unsafe` import in the Worker.

## Tests run

- `go test ./internal/compatflags ./internal/features ./hostd/gen -count=1`: ok
  (`TestExperimentalMatchesUpstream` fetched upstream's capnp: 45 flags, equal).
- `go test ./mgmt -run 'TestDeployRefusesExperimentalFlags|TestWorkloadReplicas|TestRT_Hr2group' -count=1`: ok.
- `go test ./hostd -run TestFeaturesExperimentalFlags`, `go test ./cmd/tw -run 'TestUpSaved|TestCLIReferenceUpToDate'`, `go test ./internal/hostdrun`: ok.
- `make api`, `make cli-docs`: regenerated and committed.
- `make lint`: ok (api-check, compat-check, changes-check, vet, check-secrets).
- `make test` (full suite, run locally in this worktree after merging
  `origin/master`; the mbp runner's slots were both busy): exit 0, every
  package ok, no failures.
- Skipped: `make e2e-compat` (Docker/credentials; the change touches how
  a project's `compatibility_flags` are accepted, not bundling or
  bindings). `make test-pg` not run: no store or migration change (the
  nightly runs it for `mgmt/` PRs).

## Gaps and follow-ups

- `tw dev` runs the same hostd code with the default policy, so a local
  app with an experimental flag fails with `deploy_failed` and the
  `experimental_flag` detail; there is no `tw dev --allow-experimental-flags`
  yet. Add it if a local-dev use shows up (Python Workers, #292, may want
  `python_workers_development`).
- The hostd-side refusal surfaces through the deployment's `failed` status
  (`tw deploy` prints `deploy_failed` with the `experimental_flag: …`
  detail line); mgmt's early refusal is the typed `experimental_flag` the
  developer normally sees.
- The upstream-list test needs network; offline it skips, and the binary
  test (`TestExperimentalRefusedByWorkerd`) still pins the listed flags.
