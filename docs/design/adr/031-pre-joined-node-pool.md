# ADR-031: Pre-joined node pool: assign an already-registered node to a new object by rename (and, if needed, retag)

**Date:** 2026-11-13
**Status:** Accepted by Ross (2026-11-14): build variant B. **Implemented** on tailworks `fast-identity` (`tw-hostd -pool-nodes`, default 1 per hot class). Measured on production control: create → first request on the object's own node 232 ms p50 / 297 ms p95 (was 1.7 s / 4.5 s with the warm-key pool, 7.0 s / 15.9 s before). See tailworks STATUS.md "Fast identity, Phase 3". Variant A is not built.

**Context.** Measured on tailnet 1 with production control (tailworks STATUS.md, "Fast identity"):
- After the warm-key pool, the path the key pool can't touch is the join itself: control key fetch ~0.49 s, Noise dial + register ~0.49 s, first map ~0.2 s and netcheck ~0.1 s, about **1.3 s on a clean run**.
- Registrations also stall: **+2.1–5.4 s** on 9–10 of 20 objects per run. In the last runs this was strictly every other object at one create per ~4 s, which looks like control-side pacing of registrations (a hypothesis, not verified).
- So a new object's own node is ready 1.3 s (p50) to 4.2 s (p95) after create, and 6+ s in bad runs. Compute is ready in 13–24 ms.
- Operations on an *already joined* node, measured from the call until a peer's netmap shows the change (n=5 each, all seen, node reachable throughout):
  - node changes its own hostname (LocalAPI `EditPrefs`): **209 ms p50, 337 ms max**; no API call;
  - admin rename (`POST /device/{id}/name`): 258 ms p50, but **5.1 s** once in 5 (the API write-path tail, as for CreateKey and device deletes); an admin-set name also overrides the node's own hostname for good;
  - retag (`POST /device/{id}/tags`, add or remove one tag): **185–190 ms**, 10/10.

**Proposal.** hostd keeps a small pool of pre-joined, idle nodes. On an object's first use it hands one over instead of joining a new one: the node takes the object's hostname, gets the object's tags (variant A only), and mgmt records the assignment and signs the node certificate. Expected create → own node ready: **~0.2–0.35 s**, with no registration on the critical path.

**Variants.**
- **A. One neutral pool per (host, tenant).** Nodes join as `tw-pool-<rand>` with a pool tag (e.g. `tag:tw-pool`, owned by `tag:tw`) that has **no grants**, so an idle pool node reaches nothing and nothing reaches it. Assignment = retag to the object's tags + hostname change.
  - Fewest devices.
  - Needs device-write scope on the credential that assigns (API key: yes; OAuth: Devices › Core › Write; WIF: the lifecycle credential), and tagOwners must let that credential set every object tag (minting already needs this).
  - A ~190 ms window in which peers still see the old tags.
- **B. One pool per (host, tenant, tag set).** Nodes join with the class's tags; assignment = hostname change only (209 ms, no API call, no privileged write, no tag window).
  - Costs devices per tag set, and those nodes are visible to everything the class's grants allow while idle (they serve nothing: hostd routes no traffic to an unassigned node).
- **C. Don't build it.** Keep the warm-key pool only and accept the join (1.3 s p50, control-side tails).

**Open problems, and how each variant meets them.**
1. **Hostnames are the object ID (MagicDNS).**
   - A node-side hostname change propagates in ~0.2 s and is the preferred mechanism: no API tail, and it doesn't pin an admin name.
   - Collisions: if the object's name is taken by a stale device, control suffixes it (`-1`), exactly as a fresh join would today; hostd must read the final MagicDNS name back from the netmap before reporting ready.
   - Never use the admin rename on the hot path.
2. **Tags are fixed at join.**
   - False for tagged nodes: the API retag works and propagates in ~190 ms.
   - The object must not serve until peers' packet filters reflect its tags. Measured here: peers' *tag lists* update in ~190 ms. Not yet measured: that peers' **packet filters / CapMap** (grants) update at the same time. Verify before building A.
   - Variant B avoids the question.
3. **Node certificates bind the stable node ID (ADR-010/025, RT-01).**
   - Unchanged: mgmt signs the certificate at assignment (the "ready" step) with the node's stable ID and the *final* tags and `lim`.
   - A pool node has no certificate, so receivers fail closed on it (no caps) until assignment.
   - The stable ID predates the object; receivers already key on the certificate, not on join time.
4. **Audit names nodes.**
   - Two records: a tenant-level `pool_join` (the registration: it spends a registration-throttle token and is a mint), and an `assign` record on the object (object ID, stable node ID, old and new hostname, tags), in the same transaction as the object's status change (H22).
   - `tw audit <object>` then shows which pre-joined node became the object.
   - Flow logs before assignment carry the pool hostname; with no grants (A) or no routing (B) there should be no such flows, and hostd can assert it.
5. **Costs.**
   - Every pool node is a device: plan limits, and every peer's netmap is bigger.
   - Registrations happen ahead of demand: size the pool from the create rate, as for keys.
   - Idle tsnet memory on the host.
   - Ephemeral objects need ephemeral pool nodes (separate pools).
   - A host restart re-uses pool nodes from saved state (no new registration).
6. **Per host.** A pre-joined node lives in one host's state directory, so the pool is per host. It composes with fast-vm's per-object VM pool (one VM per object): hostd hands out a node and a VM together on first use.
7. **Kill and revocation.** An assigned node is an object node in every respect: kill, device delete and tombstones are unchanged. An idle pool node that is no longer needed is deleted (not just logged out) so it can't be reused outside the pool.

**Recommendation (lead).** Build B first, for the hottest classes only (the per-class cap is 1–2 nodes per host): it needs no new credential scope and has no tag window. Revisit A after measuring grant propagation (open problem 2) and when device counts matter. Keep the warm-key pool for everything the node pool doesn't cover.

**Measurements still owed before building.**
- Grant (packet filter / CapMap) propagation after a retag (A).
- MagicDNS collision behaviour on rename.
- Whether control's registration pacing (finding above) is per tailnet, per credential or per IP: it decides how fast a pool can refill.
- Headscale's support for node-side renames and the tags API (ADR-002).
