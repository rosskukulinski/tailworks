# Changelog

What changed in each twd release, newest first. This file is generated
from the release catalog (`internal/catalog/releases/`, written from the
fragments in `changes/released/`); don't edit it by hand. How versions
work and what may break: [docs/RELEASING.md](docs/RELEASING.md#versions).
What may run together, and how to upgrade:
[docs/upgrading.md](docs/upgrading.md).

## v0.2.0 (2026-11-15)

workerd 1.20261114.1.

### Breaking

#### observability-logs-metrics-traces-20

`twd logs <workload>` now prints one structured line per request, with filters (`--errors`, `--status`, `--trace`, …) and `--json` request records. Pass `--console` for the app's raw output, which used to be the default. twd also gains Prometheus `/metrics` and OTLP export of logs, metrics and traces (`twd telemetry`).

Operator action: scripts that read `twd logs` output need `--console`. twd-hostd and twd-mgmt now serve `/metrics` on port 9464 of their tailnet node by default (`-metrics-port 0` turns it off); scraping it needs a policy grant.

### Security

#### review-fixes

Security fixes from review. `twd up` runs as a single process by default (mgmt with the host agent embedded; `--separate-host` keeps the old layout), and twd-mgmt serves an admin UI at `/ui/`.

### Upgrade actions

#### observability-logs-metrics-traces-20

`twd logs <workload>` now prints one structured line per request, with filters (`--errors`, `--status`, `--trace`, …) and `--json` request records. Pass `--console` for the app's raw output, which used to be the default. twd also gains Prometheus `/metrics` and OTLP export of logs, metrics and traces (`twd telemetry`).

Operator action: scripts that read `twd logs` output need `--console`. twd-hostd and twd-mgmt now serve `/metrics` on port 9464 of their tailnet node by default (`-metrics-port 0` turns it off); scraping it needs a policy grant.

#### workflows-approvals

Cloudflare **Workflows** (wrangler `workflows`) run on workerd's built-in engine, and `step.waitForApproval` is decided by tailnet identities holding `twd.dev/cap/approve` (`twd approvals`, `twd approve`; ADR-040).

Operator action: workerd moves to 1.20261114.1.

### Features

#### agent-first-dx-issue-25

Coding agents can drive twd: `twd agent setup` installs skills, `--json` output is a stable, documented contract (docs/cli-json.md, `schema/cli.schema.json`), errors carry `hint` and `doc_url`, `twd docs` searches the docs offline, and twd-mgmt serves an MCP server at `/mcp` (ADR-035).

#### critique-developer-experience-admin-deployers

Worker secrets (`twd secret`, ADR-034), `twd logs` from a linked machine, and many developer-experience fixes.

#### d1-databases

**D1** databases (wrangler `d1_databases`) with wrangler-layout migrations: `twd d1` creates, queries, exports and migrates them (ADR-038).

#### design-component-upgrade-release-process

You can now plan, check and undo upgrades (ADR-041).

- **Versions everywhere.**
  - Every binary carries the release version (`--version`).
  - Hosts report their version, protocols and features to mgmt, and mgmt
    replicas record their own versions.
  - `/v1/status` shows every version running in the fleet.
- **Skew policy.** A host may run one release line behind mgmt, but never
  ahead of it.
  - mgmt refuses a host outside that window with `version_skew`, and places
    nothing new on it.
  - The CLI warns you when it is too old or too new for mgmt.
- **Numbered mgmt migrations.**
  - The store records each migration it applies in `schema_migrations`.
  - A binary older than the store's minimum version refuses to start, so a
    downgrade can't corrupt the store.
  - `twd-mgmt migrate --check` lists the pending migrations.
- **Feature-gated placement.** A deployment that uses KV, R2 or D1 goes
  only to hosts that advertise that feature. If no host does, the deploy
  fails with `feature_unavailable`.
- **Host cordon and drain.** `twd host cordon`, `uncordon` and `drain`
  take a host out of placement and move its workloads off it before
  maintenance.
- **`twd upgrade`.**
  - It plans the upgrade, then backs up the store and custody together.
  - It installs and restarts the new release, then health-checks it.
  - It rolls back automatically if the health check fails.
  - `install.sh` keeps the previous install as `bin.prev` and `share.prev`,
    and `install.sh --rollback` restores it.
- **Release catalog.** twd-mgmt embeds the notes and upgrade facts of every
  release up to its own. `twd release import` adds a newer one, so
  `twd upgrade --plan` works offline.
- **Upgrade runs.**
  - mgmt drives a fleet upgrade host by host, starting with a canary and
    health-checking each host before the next.
  - The run keeps going if the CLI disconnects: it is stored in mgmt and
    driven by whichever replica holds its lease.
  - A host started with `twd-hostd -self-upgrade`, under a service manager,
    downloads the release tarball from mgmt, installs it and restarts.
    Other hosts show an operator step, and the run waits for them.
  - `twd upgrade status`, `pause`, `resume` and `abort` control the run.
- **Versioned data formats.** `objects.json`, node `object.json`,
  `twd.json` and the R2 index entries now carry a version. A twd reads older
  formats and refuses newer ones.

Operator action: none for a homelab. For a fleet, upgrade twd-mgmt before
any host. Hosts that predate version reporting count as 0.1.x, so they stay
within the window of a 0.2 twd-mgmt. See docs/upgrading.md.

#### implement-adr-15-head-scale

twd runs on a self-hosted **Headscale** control plane (ADR-015): `twd up --control=headscale` and `twd tenant add --control=headscale`. It needs Headscale v0.29 or later. There is no Funnel (public routes) and there are no Tailscale Services there; see docs/headscale.md.

#### implement-do-lease-locks-issue-26

Distributed locks and leader election on Durable Object leases: measured guarantees in docs/locks.md, and a `lock` template whose fencing tokens carry the lease epoch.

#### implement-queues-issue-18

Cloudflare **Queues** (wrangler `queues`): producers, push consumers, retries and dead-letter queues, on a sealed, lease-fenced broker in twd-mgmt (`twd queues`; ADR-039).

#### platform-stores

Platform stores: a SQLite Durable Object per store, backed by a fenced, encrypted journal in twd-mgmt, so a store survives restarts and moves between hosts. KV, D1, R2 and Queues build on it.

#### r2-bucket-binding-issue-16

**R2** buckets (wrangler `r2_buckets`): the index is a platform store, and the bytes go to a host `fs` or `s3` backend (`-r2-backend`; `twd r2`; ADR-036). Bytes on the `fs` backend stay on their host.

#### workers-kv-binding-issue-15

**Workers KV** (wrangler `kv_namespaces`): the KV API inside Workers and `twd kv` for operators. Read-your-writes on the namespace's host (ADR-037).

#### workflows-approvals

Cloudflare **Workflows** (wrangler `workflows`) run on workerd's built-in engine, and `step.waitForApproval` is decided by tailnet identities holding `twd.dev/cap/approve` (`twd approvals`, `twd approve`; ADR-040).

Operator action: workerd moves to 1.20261114.1.

#### wrangler-cli-integration-issue-19

twd works with the wrangler CLI: `wrangler.toml`, `--env`, and apps bundled by wrangler (`--wrangler`, `--from-wrangler-out`); see docs/wrangler.md.

### Fixes

#### fix-web-ui-refresh-jitter-restyle

Admin UI: no more jitter on refresh, a more readable layout, and it uses only the public `/v1` API.

## v0.1.0 (2026-11-15)

workerd 1.20261108.1.

### Features

#### initial

First release. It includes `twd up` (the all-in-one homelab mode),
`twd dev`, and twd-mgmt and twd-hostd on the process and microVM tiers,
running the pinned workerd.
