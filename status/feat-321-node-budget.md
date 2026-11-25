Last update: 2026-11-24 PT. Tailnet node budget (issue #321): docs, the estimate in `tw policy` and `tw deploy`, the count in `tw ls` and `tw doctor`.

# Tailnet node budget (#321)

## What's built

- **Docs.** `docs/limits.md` "Tailnet node budget": what an app uses in
  nodes, what else is on the tailnet (previews, pool nodes, tailworks'
  own), when a node goes away, and tailworks' own two limits on the count.
  It says the Tailscale plan's device limit applies and links the public
  pricing page; it quotes no plan numbers. `docs/concepts.md` "Object" has
  the sentence and the link. `docs/cli-json.md` documents the `nodes`
  field.
- **`tw policy` and `tw deploy` preflight.** One line: what the app uses
  and what the tenant has now, then a `!` line per warning.
- **`tw ls`** has a `NODES` line under `TENANT`; **`tw doctor`** has a
  `node_budget` check (group Tailnet), `warn` near a limit.
- **`--json`.** `nodes` on `tw policy`, `tw deploy` and `tw ls`
  (`schema/cli.schema.json` `$defs/nodeBudget`).
- **Guidance.** `docs/recipes.md` "Choose which classes get their own
  node".

All the logic is in `cmd/tw/nodebudget.go`. The shared files got one field
or one call each (`app.go`, `policyplan.go`, `ops.go`, `doctor.go`,
`doctor_render.go`). `cmd/tw/main.go`, `cmd/tw/up.go` and the README are
untouched.

## No new API

- The count: `GET /v1/objects?tenant=…&state=<live states>` (role
  `tenant:read`), counted by namespace in the CLI.
- The quota: `maxObjects` from `GET /v1/tenants/{id}`.
- Host capacity: `capacityNodes` and `liveNodes` from `GET /v1/hosts`.
  That endpoint is admin only, so a linked developer machine gets the
  count and the `maxObjects` warning, not the capacity warning.

## The counting rule, as the code has it

- One workload node per workload, when `tailnet.tags` is set, which the CLI
  requires (`cmd/tw/app.go` `loadAppWith`; `mgmt/bundle.go`
  `TwConfig.Classes`). It is one node per workload, not one per host: a
  workload runs on one host under a lease.
- `"replicas": N` is N workloads, each with its own workload node
  (`mgmt/workload_replicas.go`). A preview is another workload with its own
  node (`mgmt/preview.go`).
- `"public"` routes add the `_public` namespace and one public node
  (`mgmt/public.go` `withPublicNamespace`, `hostd/public.go`).
- One node per object of a class in `tailnet.objects` with tags, in facet
  mode and in plain-DO mode (`mgmt/bundle.go` `Classes`;
  `docs/egress-modes.md`: ingress identity is the object's own node in
  both).
- `"node": false` is the only setting that gives a class no node
  (`mgmt/bundle.go` `Classes` skips it; `hostd/bundle/bundle.go` sets
  `Class.Node = false`).
- Pool nodes are mgmt objects in `_pool`, one per hot class per host by
  default (`hostd/poolnodes.go`; `internal/hostdrun` `DefaultPoolNodes`).
- Every one of these is a live mgmt object and counts against the tenant's
  `maxObjects` (`mgmt/service.go` `CreateObject`: `CountLive(tenant, "")`).
  mgmt's and the hosts' own nodes are not objects and are not counted.
- A host's `liveNodes` is its ready objects for every tenant
  (`hostd/host.go` `heartbeatRequest`). `capacityNodes − liveNodes` is
  placement's free capacity and it needs 1 (`mgmt/placement.go`
  `freeUnits`, `ineligible`). Nothing refuses a new object of a workload a
  host already runs because of capacity.

## Where the code differs from what the issue assumed

1. "A workload is one node per host": it is one node per workload (per
   replica), wherever it runs.
2. "Classes not listed share the workload's node (plain-DO mode)":
   plain-DO mode (`"egress": "workload"`) still has a node per object. The
   setting that saves nodes is `"node": false`. The recipe and the docs say
   so.
3. A class with a wrangler binding and no `tailnet.objects` entry is not
   the same as `"node": false`. `hostd/bundle` treats it as a class with a
   node (`Node: true`, facet mode), mgmt has no namespace for it, and a
   create is `404 unknown_namespace`. It costs no nodes either way.
   `docs/tailworks-jsonc.md` says a misspelt `tailnet.objects` makes every
   class "an ordinary Durable Object with no node of its own". I did not
   run such an app to see which is right for the object itself, and did not
   change that page.
4. `-capacity-nodes` is a placement input, not a cap on a tenant. The
   limit that refuses objects is the tenant's `maxObjects` (default 1000),
   so the warning covers both.

## Not verified

- Whether every replica of a workload with `"public"` routes gets its own
  public node. The estimate assumes one per replica (each replica is
  deployed through `Deploy` with the same bundle); I read the code and did
  not run it.

## Tests

Run 2026-11-24 on the branch at `origin/master` a7a7393c plus this change.

| Command | Result |
|---|---|
| `go test ./cmd/tw/ -run 'TestEstimateAppNodes\|TestCountNodes\|TestNodeWarnings\|TestNodeBudgetFact\|TestCLINodeBudget' -count=1 -v` | pass (5 tests) |
| `go test ./docs/... ./schema/...` | pass (`TestLimitsDocCitations` resolves the new `cmd/tw/nodebudget.go:nodeBudgetWarnPct` citation) |
| `make lint` | pass |
| `make changes-check` | pass ("needs: cli-json; found in changes/feat-321-node-budget.md") |
| `sh ~/.claude/scratch/mbp-test.sh test` (`make test` on the test machine) | pass: exit 0, 127 packages ok, 0 FAIL; `cmd/tw` 279 s |

New tests, in `cmd/tw/nodebudget_test.go`:

- `TestEstimateAppNodes`: workload only; classes with tags, plain-DO and
  `"node": false`; replicas with public routes; a preview.
- `TestCountNodes`: live objects by namespace; killed, failed and
  transferred ones are not counted.
- `TestNodeWarnings`: under the margin, at 80% of `maxObjects`, at the
  host capacity by the tenant's count and by the hosts' own, both, and no
  limits known.
- `TestCLINodeBudget`: against a real tw-mgmt, `tw policy`, `tw ls` and the
  doctor check print the estimate and the count, carry `nodes` in `--json`,
  `tw ls --limit 1` still counts every node, and all three warn after
  `tw tenant set --max-objects 5`.

Not run:

- `make test-pg`: no `mgmt` store or migration change.
- `make cli-docs`: no command, flag or help text changed.
- The e2e suites that need a real tailnet (`e2e-real`, `e2e-compat`, …):
  the change is CLI output and docs. `tw deploy`'s new line is covered only
  through the shared `nodeBudgetOf` and `printNodeBudget` that
  `TestCLINodeBudget` runs for `tw policy`; no test runs a full
  `tw deploy` and reads the line.
