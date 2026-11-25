Last update: 2026-11-23 23:30 PT. ADR-054 drafted and built; tests below.

# require-exit-node-egress-hosts-workloads: internet egress through a tailnet exit node

## What's built

- `docs/design/adr/054-exit-node-egress.md`: ADR-054, Accepted (numbered at the merge). Open question 2 (exit-node DNS) was settled while
  building, see "Findings".
- **Host path** (`internal/hostdrun/internetpath.go`): `-worker-internet`
  and `-container-internet` accept `exit-node=<node>[,dns=<ip[:port]>]`
  beside `direct` and `off`. One host, one internet path: with exit-node
  the other flag must be `exit-node` (bare, or the same node) or `off`.
  Needs the host's own tsnet node (not `-local-dev`).
- **The path itself** (`hostd/exitnode.go`, `ExitNodeInternet`): finds the
  exit node in the host node's status (ID, MagicDNS name, host name or
  IP), selects it with `EditPrefs ExitNodeID` when it isn't, and serves
  `ContainerInternet.Dial`/`Resolve` for Workers (netd) and containers
  (sbxnet) alike. `Dial` goes through `tsnet.Server.Dial` to the IP netd
  checked. `Resolve` is DoH to the exit node's peer API over the tunnel,
  or DNS over TCP to the `dns=` resolver through the exit node. Both
  refuse with `exit_node_unavailable` (new netd code, 503) when the exit
  node is missing, not offered, offline or refuses DNS; the state is
  polled every 2 s and refreshed before use when older than 1 s. Never
  falls back to the host's network.
- **Workload requirement**: `tailnet.egress.via: "exit-node"`
  (`hostd/bundle`), mapped to the new host feature `internet-exit-node`
  (`internal/features`), which hostd advertises only when its path is an
  exit node (`hostd/versions.go`). Placement already gates on features.
- **CLI**: `tw up --worker-internet=exit-node=<node>` saved in tw.json and
  passed in both layouts (containers follow); `tw status` and `tw doctor`
  show the path and the exit node's problem (`StatusView.WorkerInternetProblem`);
  `tw dev` prints a note for a `via` workload. `docs/cli.md` regenerated.
- **Docs**: errors.md (`exit_node_unavailable`), tailworks-jsonc.md
  (`egress.via` row and section), security-model.md (Egress, trust table),
  containers.md, recipes.md, upgrading.md. `changes/require-exit-node-egress-hosts-workloads.md`
  (feature-string).
- Audit note for internet flows now names the path: "internet via exit
  node <node> (<addr>)".

## Findings

- tsnet documents exit nodes (`tsnet.go` "Using an exit node"):
  `Server.Dial` of a public IP goes through `PeerForIP`, which falls
  through to the route table where the selected exit node owns the
  default routes, so the dial is netstack over WireGuard. But when no exit
  node owns the default route, the same `Dial` uses the host's network
  (`tsdial.dialOneUser` → `SystemDial`/`net.Dialer`). Hence the pre-dial
  state check and the 1 s snapshot bound; that window is documented in the
  ADR.
- In the pinned tailscale.com, an exit node's peer API answers DNS for a
  **tagged** peer only through an extension hook that stock exit nodes
  don't register (`ipn/ipnlocal/peerapi.go` `isPeerAPIDNSAllowed`:
  `IsSelfUntagged`, else `HookReplyToDNSQueries`, registered only by
  `feature/conn25`). The host node is tagged, so DoH to the exit node
  will often get 403. That is why `dns=<resolver>` exists: plain DNS over
  TCP to a resolver *through* the exit node. No host-resolver fallback.
- tsnet's `Dial(name)` resolves with its own resolver hook, bypassing
  tsdial's exit-node DoH path; so the path resolves itself and dials IPs.
- `hostd.features()` was already config-dependent (`FeatureSelfUpgrade`),
  so a config-dependent feature fits the existing placement gate.

## Commands run

- `go build ./...`: ok.
- `go test ./hostd -run 'TestExitNodeInternet|TestMatchExitNode|TestParseExitNodeSpec'`: ok.
  The hermetic test fakes the host node's status, `EditPrefs` and dialer;
  a DoH stub and a TCP-53 stub answer from a table; every dial is recorded
  and any address other than the peer API, the resolver and the one public
  destination fails the test. Covers: select on first use, DoH over the
  tailnet dialer, dial through it, offline / not offered / gone from the
  netmap refused with no dial, reselect after deselection, DoH 403 fails
  closed with the `dns=` hint, `dns=` path, `Internet()` mode/via/problem,
  `features()` with and without the path.
- `go test ./internal/hostdrun ./internal/features ./hostd/bundle ./docs ./mgmt -run 'Feature|Placement'`: ok.
- `go test ./cmd/tw -run 'WorkerInternet|Doctor|TestCLIReferenceUpToDate|CLIJSON|AgentEval'`: ok after `make cli-docs`.
- `go test ./hostd -run 'Internet|Status|Feature|SelfUpgrade|ExitNode'`: ok.
- `CI=true go test ./docs/ -run TestADRIndex`: FAIL, as designed: the slug
  file must be numbered by the PR that merges it (AGENTS.md rule 5).
- `make deps` here, then `go test ./netd -run Internet` and
  `go test ./hostd -run WorkerInternetEgress` (real workerd): ok.
- Merged `origin/master` (60 commits; one conflict in
  `internal/features/features.go`, both sides' `Required` additions kept).
  After the merge: `make lint` ok; `go test ./internal/hostdrun
  ./internal/features ./hostd/bundle ./docs` ok; `go test ./hostd -run
  'ExitNode|Internet|Status|Feature|SelfUpgrade'` ok; `go test ./netd -run
  Internet` ok; `go test ./cmd/tw -run
  'WorkerInternet|Doctor|TestCLIReferenceUpToDate|CLIJSON'` ok.
- `make test` on the mbp runner (`mbp-test.sh test`, post-merge tree):
  ran to its last step (the `js/cli/tailworks` node tests, 3/3 and 51/51
  pass) and exited 0. The script shows only the last 40 lines, so the Go
  packages' `ok` lines were scrolled off; make stops at the first failing
  target, so reaching the JS tests means every Go package passed.

## Not run

- Anything against a real tailnet with a real exit node (`e2e-real`): no
  exit node available here. The ADR's end-to-end check (source address =
  exit node's, p50/p95 vs direct) is still to do on a real tailnet.
- Headscale with an exit node (`e2e-headscale` has none).

## Gaps / next

- `tw status --json` doesn't carry `WorkerInternetProblem` yet (it would
  change `schema/cli.schema.json`); the text output and `tw doctor` do.
- A per-tenant resolver choice (`InternetConfig.Resolve`, ADR-049 §4) and
  a per-object exit node (object identity at the exit node) stay unbuilt.
- Number the ADR on merge (`git grep ADR-054`), rename the
  file, add the README row.
