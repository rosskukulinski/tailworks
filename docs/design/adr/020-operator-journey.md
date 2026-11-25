# ADR-020: Operator journey: mgmt init, single-use host join tokens, tenant pools

**Date:** 2026-11-07
**Status:** Accepted (lead). This fills a gap Ross spotted: the operator side was missing from DX.md.
**Decision:**
- `tw-mgmt init` joins the operator tailnet as `tag:tw-mgmt`, through a login URL or WIF in CI. Its state lives in Postgres or S3, with custody in setec. It runs as N≥2 stateless replicas behind `svc:tw-mgmt`.
- Hosts join with `tw host add` → a **single-use join token with a 5-minute expiry**, minted by mgmt → `tw-hostd join --token`, after which the host persists its own node key. This implements DX-H1: no long-lived host secret.
- Tenants are assigned pools with `tw tenant set` (`max-objects`, `registrations-per-minute`).
- Day-2 verbs: `host drain`, `upgrade --plan`, `restore`, `status`, `freeze`.
- Paging conditions were defined in the design-phase DX.md journey 6b (not published; RFC §9 lists them).
