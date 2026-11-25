# ADR-013: Credentials: per-tenant WIF trust, scoped to auth keys for tailworks tags only; issuer key in KMS

**Date:** 2026-11-07
**Status:** Accepted; the WIF trust credentials and the issuer key are superseded by ADR-042 (tailworks mints with a per-tenant OAuth client, scoped the same way). The scoping, the opt-in device credential and the key properties below still hold.
**Context:** SEC-1: `devices:core` on a single platform signing key amounts to device-admin over every tenant tailnet.
**Decision:** Each tenant has its own trust and subject. The scope is `auth_keys` limited to `tag:tw-*`. Device deletion uses a separate opt-in credential. Minted keys are single-use, preauthorized, ephemeral, and expire in ≤5 minutes.
