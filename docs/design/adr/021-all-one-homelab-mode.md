# ADR-021: All-in-one homelab mode: `tw up`

**Date:** 2026-11-07
**Status:** Accepted (lead).
**Context:** The self-hosted prerequisites (setec, Postgres/S3, a KVM host, an issuer on a custom domain) are too heavy for a first run or a homelab. Ross plays all three roles there: operator, tenant admin, developer.
**Decision:**
- `tw up` runs mgmt, setec (homelab mode), SQLite leases and a Funnel-served issuer on one machine.
- That machine is the only host: microVM tier on Linux/KVM, process tier on macOS.
- It bootstraps from the machine's existing tailscaled login.
- It has two interactive consents: the policy diff, and creating the trust credential in the admin console (it opens the page pre-filled, and the user pastes back a public client ID).
- It stores no auth key.
**Trade-offs:**
- The Funnel issuer on a `ts.net` name relaxes SEC-19. That's acceptable when operator = tenant, and it was documented in the design-phase DX.md (not published). `--issuer <custom domain>` restores it.
- With local-only durability, backup is opt-in.
- Moving the store to Postgres is the path to multi-host (a SQLite→Postgres copy is not built: multi-mgmt runs `tw-mgmt -db postgres://…` directly, see [docs/services.md](../../services.md)).
**Success metric:** on a fresh Linux box already on a tailnet, the time from `curl … | sh` to the first private fetch from a deployed Worker is **≤10 minutes**, including both consents.
