# ADR-012: MVP is single-host per tenant namespace; v1 adopts upstream workerd cluster via public hooks

**Date:** 2026-11-07
**Status:** Accepted; async backup not built, see [#38](https://github.com/rosskukulinski/tailworks/issues/38). Amended 2026-11-16:
the single-host MVP is built (one host holds a namespace's lease, ADR-011 and
ADR-028), but DO storage is a host-local directory with no encrypted backup,
no `stord`, and no per-namespace restore. A host that is lost takes its namespaces'
Durable Object storage with it (KV, D1 and R2 indexes are journaled in
mgmt, ADR-044, and Queues live in mgmt, ADR-039); back up the state directory
([INSTALL.md](../../INSTALL.md#back-up-and-restore)).
**Context:** OPS-1: workerd's cluster exits on bucket stalls of 8–12 s. OPS-6: no re-homing. OSS-3: its private layout is about to change, because celld is merging in.
**Decision:** The MVP uses stock single-process DO storage with async encrypted backup. v1 is gated on upstream hooks: an ownership-change event, degrade-instead-of-exit, and re-homing.
