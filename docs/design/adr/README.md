# Architecture decision records

One file per ADR. The design they implement is in [../RFC.md](../RFC.md).

ADR-008, ADR-016 and ADR-017 are not published here, so the numbering has gaps.

**New ADRs:** while a decision is Proposed on a branch, name the file and the
ADR by a slug (`adr/smart-placement.md`, `ADR-smart-placement`). Take the next
free number only when merging to master, and rename the file and replace the
slug everywhere in that change (`git grep ADR-<slug>`). See `AGENTS.md` rule 5.

| ADR | Decision | Date | Status |
|---|---|---|---|
| [001](001-pivot.md) | Pivot: Tailscale-native Workers-like runtime + Durable-Objects layer | 2026-11-07 | Accepted |
| [002](002-self-hosted-oss-first.md) | Self-hosted OSS first; Headscale-compatible where feasible | 2026-11-07 | Accepted |
| [003](003-workerd-js-engine.md) | workerd as the JS engine | 2026-11-07 | Accepted |
| [004](004-node-core.md) | Node core: Go (tsnet) now, keep it swappable for tailscale-rs | 2026-11-07 | Proposed |
| [005](005-one-tailnet-node-per-workload.md) | One tailnet node per workload; multi-tailnet hosts; management plane + tenant data planes | 2026-11-07 | Accepted |
| [006](006-every-object-gets-own-tailnet-identity.md) | Every object gets its own tailnet identity | 2026-11-07 | Accepted |
| [007](007-microvm-isolation-firecracker-host-side-tailscale.md) | microVM isolation (Firecracker) with a host-side "Tailscale NIC", tailvisor-style | 2026-11-07 | Accepted |
| [009](009-hybrid-design.md) | Hybrid design: B's control/security model, A's upstream alignment | 2026-11-07 | Accepted |
| [010](010-behalf-delegation-node-bound-per-request.md) | On-behalf-of and delegation as node-bound, per-request, single-hop assertions; no bearer tokens | 2026-11-07 | Accepted |
| [011](011-leases-conditional-writes-durable-store.md) | Leases as conditional writes on a durable store; stateless controller replicas; no Raft | 2026-11-07 | Accepted |
| [012](012-mvp-single-host-per-tenant-namespace.md) | MVP is single-host per tenant namespace; v1 adopts upstream workerd cluster via public hooks | 2026-11-07 | Accepted |
| [013](013-credentials.md) | Credentials: per-tenant WIF trust, scoped to auth keys for twd tags only; issuer key in KMS | 2026-11-07 | Accepted |
| [014](014-local-dev-evaluates-real-policy.md) | Local dev evaluates real policy; outbound dependencies are declared | 2026-11-07 | Accepted |
| [015](015-headscale-profile-framework-scope.md) | Headscale profile and framework scope | 2026-11-07 | Accepted |
| [018](018-use-setec-node-key-dek-custody.md) | Use setec for node-key/DEK custody; borrow thundersnap's lease + state-broker pattern; don't depend on thundersnap | 2026-11-07 | Proposed |
| [019](019-prototype-driven-data-plane-rules.md) | Prototype-driven data-plane rules | 2026-11-07 | Accepted |
| [020](020-operator-journey.md) | Operator journey: mgmt init, single-use host join tokens, tenant pools | 2026-11-07 | Accepted |
| [021](021-all-one-homelab-mode.md) | All-in-one homelab mode: `twd up` | 2026-11-07 | Accepted |
| [022](022-agent-sdk-support-plan.md) | Agent SDK support plan; MCP is stateless | 2026-11-07 | Accepted |
| [023](023-linux-process-tier-sandbox.md) | Linux process-tier sandbox: Landlock + seccomp, no unix sockets for workloads | 2026-11-08 | Accepted |
| [024](024-microvm-privilege-model-persistent-encrypted-storage.md) | microVM privilege model and persistent encrypted storage | 2026-11-08 | Accepted |
| [025](025-node-certificates-make-spawn-lim-real.md) | Node certificates make spawn `lim` real; audit provenance; host token | 2026-11-08 | Accepted |
| [026](026-keyless-default.md) | Keyless by default: tailnet identity for every twd connection; WIF for minting; API keys only as an explicit fallback | 2026-11-08 | Accepted |
| [027](027-spec-first-rest-v1.md) | Spec-first REST v1: OpenAPI 3.1 as the source of truth, real CRUD, pagination from day one, tsnet-only transport | 2026-11-08 | Accepted |
| [028](028-database-lease-clock-hosts-fence-themselves.md) | The database is the lease clock, and hosts fence themselves for stateful namespaces | 2026-11-11 | Accepted |
| [029](029-public-ingress-over-funnel-through-netd.md) | Public ingress over Funnel, through netd on a stable per-workload public node; tsidp deferred | 2026-11-13 | Proposed |
| [030](030-microvm-snapshot-restore.md) | microVM snapshot restore: one shared base per host, private guest pages, no cross-tenant page sharing, hourly base rotation | 2026-11-13 | Accepted |
| [031](031-pre-joined-node-pool.md) | Pre-joined node pool: assign an already-registered node to a new object by rename (and, if needed, retag) | 2026-11-13 | Accepted |
| [032](032-containers-code-sandboxes.md) | Containers and code sandboxes: Cloudflare's `ctx.container` on the microVM tier, one VM per container, attached to its DO, no tailnet reach by default | 2026-11-13 | Accepted |
| [033](033-smart-placement.md) | Smart placement: put a workload on the host with the best measured tailnet path to what it connects to | 2026-11-13 | Proposed |
| [do-split](do-split.md) | Replicated Workers with single-homed Durable Objects; stubs forwarded to the owning host | 2026-11-14 | Proposed |
