# ADR-do-split: Replicated Workers with single-homed Durable Objects; stubs forwarded to the owning host

**Date:** 2026-11-14
**Status:** Proposed (branch `design/do-split`). Questions for Ross are at the end.
**Builds on:** ADR-028 (lease clock, self-fencing), ADR-033 (smart placement),
twd PR #2 (Tailscale Services) and PR #10 (workload replicas).
**Relation to ADR-012 / RFC §4.9 "v1":** this ADR does **not** change the
storage model. DO storage stays stock, single-process and local to one host.
It adds *routing*, not replication, so it needs none of the upstream workerd
hooks (WD-1/2/4). workerd cluster mode stays the path to host-loss recovery
with RPO 0.

## Context

- **Cloudflare's model.**
  - A Worker is stateless, so it runs everywhere: any request, any isolate,
    any location.
  - A Durable Object instance lives in **exactly one place**. Its location
    is chosen at first access, near the requester (`locationHint` can steer
    it), and it doesn't move.
  - `env.NS.get(id)` from a Worker anywhere returns a stub, and calls on it
    (`fetch()`, JS RPC, WebSocket upgrades) are routed to wherever the
    instance lives.
- **twd today.**
  - A workload (the Worker plus its DO classes) runs on **one host**, under
    one namespace lease (ADR-012). DO calls from the Worker are in-process:
    `env.NS.getByName()` resolves, in the trusted platform (`platform.js`
    `objectStub`), to the local `Host_<Class>` platform DO, which runs the
    tenant class as a facet.
  - Every DO instance also has its own tailnet node on that host (ADR-006).
  - PR #10 lets a **stateless** workload run on N hosts. Replica *i* is its
    own workload `<w>--r<i>` with its own lease, and all replicas share one
    Tailscale Service. Bundles with DO classes are refused there, because
    nothing routes a DO call to the one host that holds the instance's
    state.
  - The mgmt object record has no host. It has `Lease`, the owning
    workload's lease name; the lease holder is the host.
- **The gap:** an app with a Worker front end plus DOs (chat rooms, agents,
  per-user state) can't scale the front end or survive the loss of its one
  host for new sessions. Users expect Cloudflare's split.

## Decision (proposed)

1. **Opt-in shape.** A workload with `"replicas": N` and DO classes is
   accepted only with `"objects": "single-homed"` (name to be decided;
   see Q2) in twd.jsonc. That flag acknowledges the semantics: each DO
   instance lives on one replica's host, and is unavailable while that host
   is down. Without it, PR #10's refusal stays.
2. **One identity per instance, owned by one replica.**
   - A DO instance's identity is `(tenant, group workload, class, name)`,
     where the group is the workload, not the replica. Two replicas that
     touch `Counter/room-1` reach the **same** object, the same node and the
     same storage. Today the object ID hashes the workload name, so
     `web--r2/Counter/room-1` would be a different object; that has to
     change to the group name.
   - **Ownership** is the object record's `Lease`, set to the owning
     replica's lease (`tenant/web--r2`). The replica's epoch is therefore
     the instance's fencing token, and ADR-028 applies per replica
     unchanged.
3. **First-touch placement.**
   - The first `getByName(name)` from any replica asks mgmt to create the
     object (the existing `CreateObject`, which is idempotent per
     identity). The calling replica becomes the owner, so the instance sits
     next to its first caller, like Cloudflare.
   - When two replicas race on the same name, one create wins and the
     other gets the winner's record, and forwards.
   - Later: let ADR-033 choose the owner, e.g. the replica with the best
     path to the class's declared `connects`; and map `locationHint` to a
     preferred replica host.
4. **Directory.**
   - **Lookup:** hostd resolves `(class, name)` to its owner (replica, host
     and the object's node). It caches the answer, keyed by the owner's
     epoch, and asks mgmt (`GetObjectByName` plus the lease) on a miss.
   - **Invalidation:** a cached entry is dropped when the owner's epoch
     changes (the assignment and heartbeat already carry epochs), and on any
     typed `not_owner` refusal from the owner.
   - **No owner → no write:** if the owner's lease has expired (host lost),
     calls fail with a typed `503 owner_unavailable` that says which host.
     Nothing ever re-creates the instance elsewhere with empty storage,
     except a confirmed takeover (ADR-028 §3, `--force-takeover`), which
     moves ownership with empty state and is audited.
5. **Stub forwarding.** In the trusted platform, `objectStub` returns:
   - the local `Host_<Class>` stub when this replica owns the instance
     (today's in-process path, no extra hop);
   - otherwise a `RemoteStub` (a platform `WorkerEntrypoint`) that forwards:
     - **`fetch()`**, including streaming bodies and **WebSocket upgrades**;
     - **JS RPC with structured-cloneable arguments and results**: method
       name plus V8-serialized args. This is required in phase 1, because
       the Agents SDK calls `setName()` over RPC before the first `fetch`.
       Pass-by-reference RPC (stubs, functions, `ReadableStream` arguments)
       is refused with a typed `rpc_not_forwardable` error.
   - Forwarding covers every caller of `objectStub`: the Worker, other DOs
     (`NsProxy`), `/_twd/o/<Class>/<name>/` on the workload node, and public
     ingress routes (ADR-029).
6. **The forwarding channel (Q1 decides; recommendation first).**
   - **(a) Recommended: host to host over the operator tailnet, signed.**
     - hostd B sends `{tenant, group, class, name, original cf.tailscale
       caller blob, actingFor, ts, nonce}` to hostd A's node, signed with
       B's registered host key (`AuditPubKey`, ed25519).
     - A verifies three things: the signature; that B currently holds the
       lease of a replica of the same group (from assignments mgmt sent A,
       or one lookup); and replay protection (the same nonce window as
       RT-05).
     - A then dispatches to the owned instance with the **original caller's
       identity**, so `getCaller()` inside the DO sees the human or agent
       that called the Worker, as it does on a single host.
     - Needs one new operator-policy grant, `tag:twd-host → tag:twd-host`
       on the forwarding port. Needs nothing in tenant policy.
     - Trust widening: a compromised host can forge callers to DOs of the
       tenants it runs *replicas* of. It can't for other tenants (lease
       check), and it can already act as any object it hosts.
   - **(b) Alternative: the DO's own node on the tenant tailnet.**
     - Replica B's workload node calls the DO's node. netd's WhoIs sees the
       *replica* as the caller, so the original caller's identity is lost
       unless an on-behalf-of assertion is added (ADR-010, RFC §4.6; not
       implemented yet).
     - It also needs tenant grants from the workload tags to every DO
       class's tags.
     - Consistent with "every hop is a tailnet hop", but it changes what
       `getCaller()` returns inside DOs and adds policy burden. Not
       recommended until OBO assertions exist.
7. **Fencing becomes per role.**
   - ADR-028 fences a *stateful* workload whole: hostd undeploys it when it
     can't heartbeat. In this mode each replica is stateful, because it owns
     instances. Fencing the whole replica would also stop its stateless
     Worker.
   - So the fence stops only the **owned DO instances**: `Host_<Class>`
     refuses with `503 owner_fenced`, and its alarms pause. The replica's
     Worker keeps serving, and its remote stubs keep working toward owners
     that are healthy.
   - The timings stay ADR-028's: fence 30 s, lease 45 s.
8. **What stays per host.**
   - Storage, alarms (`Host_` alarm delegation), containers (ADR-032 D9:
     a container runs on its parent DO's host) and the DO's own tailnet node
     all live on the owner. Kill already targets the object's host through
     its lease holder.
   - Scale-down of a replica that owns instances is refused with
     `409 replica_owns_objects` unless `--force` (audited). Its instances
     would become unavailable, because storage doesn't move in this ADR.

## Phases

| Phase | Scope | Exit criteria |
|---|---|---|
| P1 | Opt-in flag; group identity; first-touch ownership; directory with an epoch-keyed cache; `RemoteStub` for `fetch` + WebSocket + structured-clone RPC; channel (a); per-role fence; typed errors (`owner_unavailable`, `owner_fenced`, `rpc_not_forwardable`, `replica_owns_objects`) | Hermetic: two hostd instances on testcontrol. A Worker on B uses a DO owned by A via fetch, RPC (Agents SDK `getAgentByName`) and WebSocket, with `getCaller()` = the original client. Real (tailnet 1, two hosts): the same, plus the owner host lost → `owner_unavailable`, and the other replica's Worker and DOs keep serving |
| P2 | ADR-033 owner placement (connects-aware); `locationHint` → preferred replica host | Placement-decision records for instance owners |
| P3 | Moving ownership *with* storage (drain-time handoff of a stopped instance's SQLite files), then host-loss recovery via the RFC's encrypted backup, or via workerd cluster mode once WD-1/2/4 land | Drain with RPO 0; host loss with RPO ≤ backup lag |

## Consequences

- Apps with DOs get Cloudflare's shape: a scalable, failover-capable front
  end, while each DO keeps strict single-writer semantics, with no
  replication protocol and no storage change.
- **Availability is per instance:** losing a host makes *its* instances
  unavailable until it returns. Other instances, and every replica's
  Worker, keep serving. It doesn't recover lost instances; that is P3, or
  cluster mode.
- **Latency:** a call to a remote instance adds one tailnet hop. That's
  about 1 ms direct on a LAN and tens of ms over DERP. First-touch keeps
  each instance next to its first caller.
- **Security:** channel (a) adds a host-to-host trust edge, bounded per
  tenant and group by lease checks. It needs the operator-policy grant, and
  replay protection reuses RT-05.
- **Code:** the work lives in new files (`hostd/dorouting.go`,
  `mgmt/doowner.go`, a `RemoteStub` section in `platform.js`), with small
  hooks in `objectStub`, `CreateObject` (group identity) and the fence path
  (AGENTS.md rule 4).
- **COMPAT:** the "per-object placement" row moves from ❌ to "first-touch;
  `locationHint` P2".

## Rejected

- **Replicating DO storage** (a replicated log, multi-writer SQLite): this
  races upstream workerd, and the RFC already rejected it (§11).
- **Pinning every instance of a class to one replica:** simpler, but one
  host then carries a whole class and loses all of it together. Per
  instance is Cloudflare's model and costs nothing extra once the directory
  exists.
- **Routing through mgmt:** this puts the control plane on the data path,
  and mgmt is stateless and replicated, not a proxy.

## Questions for Ross

1. **Channel:** (a) signed host-to-host with the original caller preserved
   (recommended), or (b) the DO's own node on the tenant tailnet, which
   waits for OBO assertions and needs tenant grants?
2. **Opt-in name and shape:** `"objects": "single-homed"`,
   `"replicas": {"count": 3, "objects": "single-homed"}`, or implicit
   whenever `replicas > 1` and DO classes exist (with the semantics only in
   the docs)?
3. **Owner choice in P1:** first-touch (Cloudflare-like, recommended), or
   ADR-033 scoring from day one?
4. **Scale-down of an owning replica:** refuse without `--force`
   (recommended), or allow and leave its instances unavailable until the
   replica returns?
5. **RPC scope in P1:** structured-clone only, refusing pass-by-reference
   (recommended), or `fetch`-only first with RPC in P1.5 (which leaves
   Agents SDK apps unsupported until then)?
