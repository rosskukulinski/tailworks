# ADR-033: Smart placement: put a workload on the host with the best measured tailnet path to what it connects to

**Date:** 2026-11-13
**Status:** Accepted (2026-11-16, built and shipped in v0.2.0: `mgmt/placement.go`;
`tw deploy` prints why it placed a workload). Placement stays per host, but it
now reads the workload's declared dependencies. (The draft cited a `DX.md`
compatibility row; the matrix is [docs/COMPAT.md](../../COMPAT.md).)

**Context.**
- Before this, `pickHost` did three things:
  1. an explicit `--host` won;
  2. otherwise it kept the live lease holder;
  3. otherwise it picked the host with the most free nodes.
- It ignored `tailnet.connects`.
- It also ignored `Host.Tiers`. A `"tier": "microvm"` workload could land on
  a host without KVM, where hostd silently ran it in the **process tier**.
  That was an isolation downgrade. It was fixed on tailworks master separately
  (`9f03c48`: `placementTier`, `409 tier_unavailable`), and smart placement
  keeps those semantics as an eligibility input.
- **Cloudflare Smart Placement**
  (developers.cloudflare.com/workers/configuration/placement, read
  2026-11-13):
  - `placement` takes `{"mode": "smart"}`, or one of `region`, `host` (L4,
    experimental) or `hostname` (L7). They are mutually exclusive.
  - It analyses request durations per location, for up to 15 min after
    deploy, and forwards requests when a location is "significantly
    faster".
  - Its status values are absent, `SUCCESS`, `INSUFFICIENT_INVOCATIONS` and
    `UNSUPPORTED_APPLICATION`.
  - It affects fetch handlers only.
- tailworks has no locations. It has hosts, and the dependency (Postgres, an
  internal API) sits on the tenant's tailnet at some path from each host:
  direct WireGuard, a peer relay, or DERP.

**Decision.**
1. **Measurement source: the tenant's own nodes on each host; no mints.**
   - Targets are on the tenant's tailnet, and hostd's own node is on the
     operator tailnet. So hostd measures from a node it **already runs**
     for that tenant: workload entry nodes first, then any ready object
     node, up to 4 vantage nodes.
   - It resolves each target in that node's netmap:
     - `tag:x` matches peers with the tag (at most 4, best kept);
     - a name matches a host name, a MagicDNS name (full or first label)
       or a Tailscale IP;
     - anything else is reported `unresolved`. Tailworks never guesses.
   - It sends up to 4 WireGuard **disco pings** (`LocalClient.Ping`,
     `PingDisco`), 150 ms apart:
     - the class is the last answered ping's: `Endpoint` → direct,
       `PeerRelay` → peer relay, otherwise DERP with its region;
     - the RTT is the lowest among pings of that class.
   - Disco pings need the peer in the netmap but no port grant. They open
     no connection and touch no workload traffic.
   - **No new key material, no throttle tokens, no devices.** A host with
     no node of the tenant reports nothing (unmeasured). Rejected options:
     - a per-(host, tenant) probe node: a mint, registration-throttle tokens, and a
       device to clean up;
     - netmap-only signals: peer endpoints and the home DERP describe the
       target, not the path from this host.
   - **Gap:** a brand-new tenant's first workload is placed by capacity.
     Pin it once (`--host`) next to the dependency; later deploys can
     measure. An opt-in probe node, through the registration throttle, is the
     follow-up if this matters.
2. **Data model.**
   - **Reports.** hostd sends `PUT /v1/hosts/{id}/paths` with, per tenant
     and target, `{peer, path, rttMs, derpRegion, ageMs}`.
     - Only the targets its assignments declare (`placementTargets`).
     - Every 60 s, and when the set changes. Not in the heartbeat.
     - Bounds: 64 tenants, 32 targets each.
     - A host may report only for tenants it runs a workload of.
   - **Storage.** mgmt stores `host_paths (tenant_id, target, host_id)` in
     SQLite or Postgres. Each report replaces the (host, tenant) rows in
     one transaction.
     - `measured_at = m.now() − ageMs`, on the store clock (ADR-028 §1).
       Host clocks never matter.
     - Placement ignores measurements older than **5 min**. Rows older than
       1 h are deleted when the host reports again, and `host rm` deletes
       the host's rows.
     - Write cost: one small transaction per host per tenant per minute,
       only while that tenant has targets on the host.
   - **On demand at deploy.** mgmt pushes `POST /v1/push/probe` to
     eligible hosts that lack a fresh measurement (parallel, ≤ 2 s
     budget), and stores the replies.
   - **Replicas.** Any replica can place: the data is in the store.
3. **Scoring.** `scoreHosts(candidates, measurements, requirements)` is
   pure.
   - **Requirements:**
     - pool, tiers, and capacity *units* (`nodes` today; `vms` and
       `hugepageMiB` for the sandbox design);
     - `pinned` (`--host`, or a sandbox next to its parent DO: it bypasses
       scoring but must be eligible);
     - `sticky` (the live lease holder);
     - `replace`;
     - mode, targets and aggregate.
   - **Eligibility:** live, not draining (there is no draining state yet,
     so the input is always false), pool, every tier, enough free units.
     The sticky holder is exempt from the capacity check.
   - **Order:**
     1. path rank (direct 3 > peer relay 2 > DERP 1 > unmeasured 0);
     2. RTT, between measured hosts only;
     3. free capacity;
     4. host ID.
     - Several targets aggregate as `worst` (default: the minimum class,
       then the maximum RTT) or `sum` (summed ranks, then summed RTTs).
     - Stale, unresolved and unreachable all count as unmeasured.
   - **Determinism:** the result is independent of input order (tested
     over 200 shuffles). Three Postgres replicas placing concurrently
     agreed on host and explanation (multimgmt S12).
4. **Stickiness and moves.**
   - **A running workload never moves on its own**: rule 2 stays.
   - Smart placement applies at first deploy, when the holder is dead, and
     on an explicit **`tw deploy --replace`**.
   - **Hysteresis**, including for `--replace`: the workload moves only to
     a better path class, or the same class with ≥ 2 ms *and* ≥ 20 % lower
     RTT. Capacity alone never moves it.
   - The move is **`MoveLease`**: one conditional write `(holder, epoch) →
     (new, epoch+1)`. Concurrent re-places have one winner; the others get
     `409 lease_changed`. The old holder's next renew fails, and it is
     revoked and stops.
   - The new host runs its normal deploy path (blue/green or serial on
     that host).
     - Cross-host is **not** make-before-break by protocol. In the
       hermetic run the new host was ready after 0.61 s and the old one
       stopped at 0.62 s.
     - A stateful move also pushes the old host at once.
   - **Stateful workloads:**
     - a move off a *live* host needs `--force-takeover`
       (`409 move_unconfirmed`): DO storage stays on the old host's disk;
     - ADR-028's guarded takeover (`takeover_unconfirmed`) is unchanged for
       dead holders.
   - **Re-mint on move.** Moving a workload re-mints its already-joined
     nodes on the new host, fenced by the new epoch; mgmt deletes the old
     device. That is one throttled registration per moved node (found by the
     e2e; also fixes the same failure in host failover).
   - **No automatic rebalancing** is built. If one is, it is off by
     default and needs the same hysteresis plus a minimum dwell time.
5. **Config.**
   - `tailworks.jsonc` `placement: {mode: "smart" | "off", targets?: [...],
     aggregate?: "worst" | "sum"}`.
   - Targets default to `tailnet.connects`.
   - **Smart is the default** whenever there are targets; stickiness makes
     it safe.
   - Cloudflare's `placement.host`/`hostname` become one target (port
     dropped). `placement.region` is ignored with a warning.
   - `wrangler.jsonc` `placement` is read if `tailworks.jsonc` has none.
   - `--host` overrides, but must still be eligible.
6. **Explainability.**
   - Deploy prints the reason, e.g. `placed on h2: tag:db direct 1.8 ms (h1:
     relayed via DERP nyc 24 ms, h3: unmeasured)`.
   - The decision (reason `pinned|sticky|smart|capacity`, summary, and the
     top 8 hosts with per-target paths) is stored with the deployment.
     `GET /v1/deployments/{id}` `placement` and `tw ls` show it.
   - The deploy audit record carries `placement.{reason, summary,
     targets}`, plus `movedFrom`/`replace` on moves.

**Consequences.**
- A Worker that declares `connects: ["tag:db"]` lands next to its Postgres
  on first deploy, provided some host already runs a node of that tenant
  that can see the db. Otherwise it is placed by capacity, and the
  decision says "no measured path".
- **Still not placed:**
  - per-object placement: every DO instance follows its workload's host;
  - automatic rebalancing;
  - a host draining state (`host rm --drain` deletes the host);
  - targets nobody on a host can see in a netmap (no grant → no peer →
    unresolved).
- Tier eligibility (tailworks `9f03c48`) is part of the filter. A live holder
  without the tier is refused with `409 tier_unavailable` unless the deploy
  is `--replace`, which moves it.

**Open (for Ross).**
- Defaults: smart on when `connects` is set; `worst` aggregate; 5 min TTL;
  60 s report period; hysteresis 2 ms / 20 %; stateful moves need
  `--force-takeover`; no automatic moves.
- Whether a pinned `--host` that lacks the tier should fail (now) or warn.
- An opt-in per-(host, tenant) probe node for empty hosts (mint through
  the registration throttle).
