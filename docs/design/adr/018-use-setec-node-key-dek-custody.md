# ADR-018: Use setec for node-key/DEK custody; borrow thundersnap's lease + state-broker pattern; don't depend on thundersnap

**Date:** 2026-11-07
**Status:** Proposed (lead). Ross asked whether thundersnap helps.
**Context:** thundersnap's `network-and-keys-design.md` independently arrives at our controller model:
- a coord node issues leases;
- the lease holder gets tsnet state from setec through coord;
- the identity follows the lease, and the old holder's processes are killed;
- policy is CapMap with a local grant-syntax fallback;
- one VM per user.
thundersnap is a self-described toy and btrfs-only. setec is a maintained BSD-3 secrets service whose access is controlled by grants (`tailscale.com/cap/secrets`), with KMS/TPM/homelab bootstrap, an audit log and backups.
**Decision:**
1. **Custody store.** Node state and per-object DEKs live in **setec** (namespaced `tw/<tenant>/<object>`). Only `tag:tw-mgmt` can access them, and the controller releases them to the lease holder. This replaces the unspecified KMS-wrapped store from ADR-009/SEC-8.
2. **Lease protocol.** Mirror thundersnap-coord's acquire/renew(TTL/3)/release and fail-safe semantics, with our addition of epoch fencing at storage (ADR-011).
3. **v1 migration and backup option.** Evaluate snapshot replication over the tailnet, as thundersnap does, for bucket-less self-hosted moves of an object's VM and SQLite.
4. **No runtime dependency on thundersnap.**
