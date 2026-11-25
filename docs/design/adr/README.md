# Architecture decision records

One file per ADR. The design they implement is in [../RFC.md](../RFC.md).

ADR-008, ADR-016 and ADR-017 are not published here, so the numbering has gaps.
Some early ADRs (and the RFC's prototype results) cite design-phase working
files under `prototype/` and `review/`; those are not published in this
repository either. What is built, and where it differs from the RFC and these
ADRs, is in the RFC's [As built](../RFC.md#as-built-v020) section.

**New ADRs:** while a decision is Proposed on a branch, name the file and the
ADR by a slug (`adr/smart-placement.md`, `ADR-smart-placement`). Take the next
free number only when merging to master, and rename the file and replace the
slug everywhere in that change (`git grep ADR-<slug>`). See `AGENTS.md` rule 5.
In CI, `docs:TestADRIndex` fails while a slug file exists or a numbered file
has no row below.

| ADR | Decision | Date | Status |
|---|---|---|---|
| [001](001-pivot.md) | Pivot: Tailscale-native Workers-like runtime + Durable-Objects layer | 2026-11-07 | Accepted |
| [002](002-self-hosted-oss-first.md) | Self-hosted OSS first; Headscale-compatible where feasible | 2026-11-07 | Accepted |
| [003](003-workerd-js-engine.md) | workerd as the JS engine | 2026-11-07 | Accepted |
| [004](004-node-core.md) | Node core: Go (tsnet) now, keep it swappable for tailscale-rs | 2026-11-07 | Proposed (Go tsnet is the only core built) |
| [005](005-one-tailnet-node-per-workload.md) | One tailnet node per workload; multi-tailnet hosts; management plane + tenant data planes | 2026-11-07 | Accepted |
| [006](006-every-object-gets-own-tailnet-identity.md) | Every object gets its own tailnet identity | 2026-11-07 | Accepted |
| [007](007-microvm-isolation-firecracker-host-side-tailscale.md) | microVM isolation (Firecracker) with a host-side "Tailscale NIC", tailvisor-style | 2026-11-07 | Accepted |
| [009](009-hybrid-design.md) | Hybrid design: B's control/security model, A's upstream alignment | 2026-11-07 | Accepted |
| [010](010-behalf-delegation-node-bound-per-request.md) | On-behalf-of and delegation as node-bound, per-request, single-hop assertions; no bearer tokens | 2026-11-07 | Accepted |
| [011](011-leases-conditional-writes-durable-store.md) | Leases as conditional writes on a durable store; stateless controller replicas; no Raft | 2026-11-07 | Accepted |
| [012](012-mvp-single-host-per-tenant-namespace.md) | MVP is single-host per tenant namespace; v1 adopts upstream workerd cluster via public hooks | 2026-11-07 | Accepted; async backup not built, see [#38](https://github.com/rosskukulinski/tailworks/issues/38) |
| [013](013-credentials.md) | Credentials: per-tenant WIF trust, scoped to auth keys for tailworks tags only; issuer key in KMS | 2026-11-07 | Accepted; WIF superseded by ADR-042 |
| [014](014-local-dev-evaluates-real-policy.md) | Local dev evaluates real policy; outbound dependencies are declared | 2026-11-07 | Accepted |
| [015](015-headscale-profile-framework-scope.md) | Headscale profile and framework scope | 2026-11-07 | Accepted |
| [018](018-use-setec-node-key-dek-custody.md) | Use setec for node-key/DEK custody; borrow thundersnap's lease + state-broker pattern; don't depend on thundersnap | 2026-11-07 | Proposed (setec is an optional custody backend; per-object DEKs not built) |
| [019](019-prototype-driven-data-plane-rules.md) | Prototype-driven data-plane rules | 2026-11-07 | Accepted |
| [020](020-operator-journey.md) | Operator journey: mgmt init, single-use host join tokens, tenant pools | 2026-11-07 | Accepted |
| [021](021-all-one-homelab-mode.md) | All-in-one homelab mode: `tw up` | 2026-11-07 | Accepted |
| [022](022-agent-sdk-support-plan.md) | Agent SDK support plan; MCP is stateless | 2026-11-07 | Accepted |
| [023](023-linux-process-tier-sandbox.md) | Linux process-tier sandbox: Landlock + seccomp, no unix sockets for workloads | 2026-11-08 | Accepted |
| [024](024-microvm-privilege-model-persistent-encrypted-storage.md) | microVM privilege model and persistent encrypted storage | 2026-11-08 | Accepted |
| [025](025-node-certificates-make-spawn-lim-real.md) | Node certificates make spawn `lim` real; audit provenance; host token | 2026-11-08 | Accepted |
| [026](026-keyless-default.md) | Keyless by default: tailnet identity for every tailworks connection; WIF for minting; API keys only as an explicit fallback | 2026-11-08 | Accepted; minting superseded by ADR-042 |
| [027](027-spec-first-rest-v1.md) | Spec-first REST v1: OpenAPI 3.1 as the source of truth, real CRUD, pagination from day one, tsnet-only transport | 2026-11-08 | Accepted |
| [028](028-database-lease-clock-hosts-fence-themselves.md) | The database is the lease clock, and hosts fence themselves for stateful namespaces | 2026-11-11 | Accepted |
| [029](029-public-ingress-over-funnel-through-netd.md) | Public ingress over Funnel, through netd on a stable per-workload public node; tsidp deferred | 2026-11-13 | Accepted |
| [030](030-microvm-snapshot-restore.md) | microVM snapshot restore: one shared base per host, private guest pages, no cross-tenant page sharing, hourly base rotation | 2026-11-13 | Accepted |
| [031](031-pre-joined-node-pool.md) | Pre-joined node pool: assign an already-registered node to a new object by rename (and, if needed, retag) | 2026-11-13 | Accepted |
| [032](032-containers-code-sandboxes.md) | Containers and code sandboxes: Cloudflare's `ctx.container` on the microVM tier, one VM per container, attached to its DO, no tailnet reach by default | 2026-11-13 | Accepted |
| [033](033-smart-placement.md) | Smart placement: put a workload on the host with the best measured tailnet path to what it connects to | 2026-11-13 | Accepted |
| [034](034-worker-secrets.md) | Worker secrets: values in mgmt custody, released only to the workload's lease holder at its epoch, text bindings in workerd | 2026-11-14 | Accepted |
| [035](035-mgmt-mcp.md) | tailworks' own MCP server on mgmt, next to `/v1`: tools run the API in process, destructive tools only from a `github.com/rosskukulinski/tailworks/cap/mcp` grant naming them | 2026-11-15 | Accepted |
| [036](036-r2-buckets.md) | R2 buckets as a platform store: the index in the journal, the bytes in an operator-configured blob backend that hostd talks to | 2026-11-15 | Accepted |
| [037](037-workers-kv.md) | Workers KV: a platform store per workload behind workerd's own `kvNamespace` binding; read-your-writes per host, writes journaled in mgmt first | 2026-11-15 | Accepted |
| [038](038-d1-databases.md) | D1 databases as platform-store Durable Objects behind workerd's own D1 API, with statement journals in mgmt | 2026-11-15 | Accepted |
| [039](039-queues.md) | Queues: a sealed, lease-fenced broker in mgmt; hostd runs push consumers through workerd's own queue binding and `queue()` dispatch; the deploy is the grant | 2026-11-15 | Accepted |
| [040](040-workflows-approvals.md) | Workflows on workerd's built-in engine; an approval is a decision by a tailnet identity holding `github.com/rosskukulinski/tailworks/cap/approve`, checked and audited by tailworks | 2026-11-15 | Accepted |
| [041](041-upgrades-and-releases.md) | One release train, declared version skew, expand/contract store migrations, feature-gated placement, mgmt-driven upgrade runs, tested upgrades | 2026-11-15 | Accepted |
| [042](042-oauth-only-minting.md) | Mint node keys with an OAuth client only; remove the API-key and WIF mint modes | 2026-11-15 | Accepted |
| [043](043-team-tenants.md) | Several tenants per tailnet, one per team: a tenant-admin capability, a per-tenant owner tag, and per-tenant name prefixes | 2026-11-16 | Accepted |
| [044](044-platform-stores.md) | Platform-owned SQLite Durable Objects with a fenced journal in mgmt back storage bindings | 2026-11-15 | Accepted |
| [045](045-observability.md) | Structured request logs, Prometheus metrics and OTLP traces, built from workerd tail events and tailworks' own hops | 2026-11-15 | Accepted (phase 1; phases 2 and 3 not built) |
| [046](046-deploy-rollback.md) | Deployment history and rollback as a new deploy of a stored bundle; data moves forward only, secrets are not versioned | 2026-11-15 | Accepted |
| [047](047-approval-notifications.md) | Tenant notification webhooks, starting with `approval_requested` | 2026-11-15 | Accepted |
| [048](048-deploy-preview.md) | A preview is a workload of its own next to the workload; promote deploys its bundle to the workload | 2026-11-15 | Accepted |
| [049](049-worker-internet-egress.md) | Workers have internet egress by default, from the host's network through netd's SSRF guard; `tailnet.egress.internet` turns it off or limits it, for Workers and containers alike | 2026-11-21 | Accepted |
| [050](050-static-assets.md) | wrangler `assets` are served by a generated router Worker in front of the app, from files in the bundle, with `_redirects`, `_headers` and assets-only projects | 2026-11-23 | Accepted |
| [051](051-pages-functions.md) | Cloudflare Pages projects (`functions/`, `_worker.js`, `_routes.json`) are compiled by `tw` onto Workers with static assets | 2026-11-23 | Accepted |
| [052](052-tcp-egress.md) | Raw TCP egress: `connect()` handlers on the platform's egress entrypoints, CONNECT tunnels to hostd authenticated in band, dialled with netd's `DialEgress` | 2026-11-23 | Accepted |
| [053](053-service-bindings.md) | Service bindings are tailnet calls from the calling node, resolved by mgmt; same-host calls take netd's in-process hop | 2026-11-23 | Accepted |
| [054](054-exit-node-egress.md) | A host can send all internet egress through a tailnet exit node, via its own node; `tailnet.egress.via: "exit-node"` lets a workload require it | 2026-11-23 | Accepted |
| [055](055-python-workers.md) | Python Workers run in a Python worker of their own behind a generated JavaScript front; Pyodide bundles are pinned, fetched once per host and read-only to the sandbox | 2026-11-24 | Accepted |
