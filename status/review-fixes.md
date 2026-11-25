# review-fixes: security fixes, DX fixes, single-process `twd up`, admin UI

Last update: 2026-11-14 PT. Rebased onto master after the mm-loop, fast-vm, fast-identity, smart-placement, Services, cron and containers merges; full `make test`, `make lint`, `e2e-local`, `test-sdk`, `test-node` green; live runs against a local control plane (corp `devcontrol`) in both layouts.

Branch `review-fixes` (one commit). Earlier history of these items is in the
commit message; this file is the status record per `status/README.md`.

## What's built

**Security (provisioning / identity layer; findings verified against a live
control plane before fixing).**
- hostd's push API on the host's tailnet node serves only kill/assignment/probe
  pushes and requires WhoIs: the peer must carry `tag:twd-mgmt` and match the
  mgmt node pinned at start (`hostd/pushauth.go`); status, purge and vm-check
  stay on the local socket. Typed `push_unauthorized`.
- `Ready`/`Remint` are fenced on the lease holder (and on the epoch when the
  host sends one, which hostd always does; upstream RT3-02 has the same rule);
  the killed-while-joining device delete runs only when the fence held (and is
  still recorded durably in the same write, F21).
- Object/class tags may not be twd's own tags (`reserved_tag`), enforced in
  deploy, preflight and spawn.
- `POST /v1/hosts` returns a one-time registration secret (hash stored in
  `hosts.reg_secret_hash`); the first tailnet `PUT /v1/hosts/{id}` must present
  it; unknown host IDs cannot self-register.
- Host-role routes are scoped to the caller's leases/assignments
  (`requireLease`, `hostScope`); the audit pubkey is pinned at first
  registration and verified at ingest; API-key files must be 0600; credential
  files are opened O_NOFOLLOW + fstat; `tag:twd` prefix boundary; 1 MiB host
  body cap.

**Developer experience.** `docs/errors.md` (every typed code), error URLs
resolve, `twd init` derives the workload tag from the app name, MCP-aware
`twd dev`, per-command `--help`, `node-workflow` says `npm install`,
`install.sh` progress, multi-tenant tests run on docker/lxd hosts.

**Single-process mode (default for fresh state).** `internal/mgmtd` and
`internal/hostdrun` are the daemons as libraries; `twd up` runs mgmt and an
embedded host agent in one process on one tsnet node. The host talks to mgmt
through an in-process transport carrying a `host` principal
(`internal/inproc`), so every per-lease authorization applies; mgmt pushes to
it in-process (`inproc://`, persisted as the host's push target). `twd up
--separate-host` keeps the two-node layout; `twd host add` is the scale-out
path.

**Admin UI.** `mgmt/ui`: server-rendered console at `/ui/` on mgmt's node
behind the same WhoIs admin capability (overview, hosts, tenants,
deployments, object lineage, audit; kill/undeploy/drain/delete), rendered
from in-process `/v1` calls under the caller's principal; browser-origin
guard (`Sec-Fetch-Site`/`Origin`, `X-Twd-UI` on mutations), CSP.
`docs/ADMIN-UI.md`.

## Verification (2026-11-14, Ubuntu 26.04 amd64, Landlock ABI 8, no KVM)

- `make test`, `make lint`, `make e2e-local`, `make test-sdk`, `make test-node`: PASS.
- Live against corp `devcontrol` (real control + API on `http://localhost:31544`,
  tailnet `example.com`, a `tag:twd` OAuth client), OAuth mint mode:
  - single process: `twd up` 0.7 s, one infra device (`twd-mgmt`) + object
    nodes, deploy → ready 0.9 s, `/ui/` 200 for a `tag:ttest` admin and
    `403 not_admin` for a `tag:db` node, cross-site DELETE `403 forbidden`,
    same-origin UI kill `202` with an in-process push, tombstone page,
    `twd down --purge`, 0 devices left;
  - `--separate-host`: host node joins with the registration secret, deploy,
    `twd kill` pushed over the tailnet to the WhoIs-pinned host node, 0 left;
  - before the fixes, an unrelated node read the host's full status dump on
    :7474 and a bundle could request `tag:twd-host`; both now refused.

## Gaps / next

- `twd tenant add` for a second tenant is apikey-only (unchanged).
- The UI shows containers/capacity only as far as `/v1` exposes them; no
  per-host object filter in the API (host page lists objects of assigned
  workloads).
- Single-mode supervisor does not restart a crashed component in-process
  (the process exits; `--service` units restart it).
