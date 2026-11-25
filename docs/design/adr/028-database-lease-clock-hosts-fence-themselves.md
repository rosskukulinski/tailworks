# ADR-028: The database is the lease clock, and hosts fence themselves for stateful namespaces

**Date:** 2026-11-11
**Status:** §1–2 Accepted and implemented (tailworks mm-loop F1/F2). **§3 re-decided by Ross (2026-11-11, second interview)** after a correction: hostd already self-fenced every workload after 9 s. Guarded moves (tailworks F4) are implemented.

**Context.** The multi-replica mgmt test loop (tailworks `e2e/multimgmt`, H10)
showed that lease expiry and host liveness use each mgmt replica's own wall
clock.
- With ≥ 10 s of skew, redeploys of a healthy workload are refused.
- With ≥ 12–15 s, a healthy, heartbeating lease holder is displaced.

Separately, hostd stops serving a namespace only on an explicit `revoke`
in a heartbeat response. A host cut off from mgmt keeps serving forever,
even after its lease has expired and been reassigned. ADR-011's
fail-static covers mgmt when the *store* is unreachable. It says nothing
about the host side.

**Decision.**
1. **One clock.** Every time-based ownership decision is made with the
   store's clock, inside the same SQL statement that reads or writes it:
   lease acquire, renew and expiry; host `last_seen` and liveness; key
   release windows; revocation windows; TTL reaping. Replica wall clocks
   are used only for informational timestamps.
2. **Skew guard.** Each mgmt replica measures its offset from the store's
   clock and reports it in `GET /v1/status` (`clock`). `ok=false` above
   2 s, or when the last sample is over 30 s old, and a warning is logged.
   It warns rather than refusing traffic: after (1), wall-clock skew no
   longer affects ownership decisions. What remains on the local clock is
   WIF token `iat`/`exp`, throttles and the policy cache.
3. **Hosts self-fence stateful workloads only; moves are guarded.**
   - **Stateful workloads** (any Durable Object class not marked
     `"fence": "never"`, as computed by mgmt from the bundle and sent in
     each assignment) are fenced by hostd. It undeploys them when no
     heartbeat has succeeded for `FenceAfter`, measured on the monotonic
     clock, and redeploys them on reconnect.
   - **Stateless Workers and `fence: never` classes keep serving** through
     mgmt and store outages. Their platform stores (KV, D1, R2) answer
     reads from the host's copy and refuse writes meanwhile (ADR-044 §8).
   - **Timings:** `FenceAfter` = 30 s, `NamespaceLeaseTTL` = 45 s,
     heartbeat every 3 s. The fence must stay below the lease minus a
     margin, so a host has stopped serving before its lease can be taken
     over. The goal is to ride out typical database failovers and mgmt
     restarts. Before this decision it was 9 s for every workload.
   - **Guarded moves:** mgmt refuses to move a stateful workload off a
     registered, unreachable host unless the host was removed or the
     deploy sets `--force-takeover` (audited). Once the old host's lease
     has expired, it has already fenced, so a confirmed takeover is safe.
4. **Margins.** The margin covers heartbeat RTT plus clock *rate* drift.
   The lease TTL on the store stays strictly longer than the host's fence
   deadline. mgmt never hands a lease to another holder before
   `expires_at` by the store clock.

**Consequences.**
- Correctness of DO state no longer depends on NTP.
- A partitioned host stops serving its stateful objects after about 12 s,
  rather than serving them while another host starts them with empty
  state. That costs availability during mgmt outages; Workers are
  unaffected.
- Self-fencing is required before an automatic failover loop is built.
- Tests: `TestMM_H10_LeaseClockSkew` must pass at 0–20 s of skew, and
  scenarios S3/S4/S8 (stall, partition, skew) must pass.
