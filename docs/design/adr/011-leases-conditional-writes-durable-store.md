# ADR-011: Leases as conditional writes on a durable store; stateless controller replicas; no Raft

**Date:** 2026-11-07
**Status:** Accepted.
**Context:** OPS-2: one Raft group per region meant losing quorum took out every tenant, with no backup. OPS-9: A's mgmt plane had no HA.
**Decision:** Per-object leases with epoch fencing are stored with S3 conditional writes or in Postgres. `tw-mgmt` replicas are stateless. When the store is unreachable, placement freezes (fail-static) rather than going down.
