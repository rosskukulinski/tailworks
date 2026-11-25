# ADR-026: Keyless by default: tailnet identity for every tailworks connection; WIF for minting; API keys only as an explicit fallback

**Date:** 2026-11-08
**Status:** Accepted (Ross). Implementation: tailworks M10. Decision 3 (WIF minting, API keys as the fallback) is superseded by ADR-042: tailworks mints with an OAuth client only. Decisions 1, 2 and 4 stand.
**Context:** The first product flow shipped the test-harness shortcut: `tw up --api-key-file`, a CLI that only reached mgmt over a local unix socket, and interactive login refused. Ross: "We shouldn't have to use an API key. We've got Tailscale identity built into the connection." The RFC (§4.4) and ADR-013 had already specified WhoIs-authenticated mgmt and WIF minting.
**Decision:**
1. **Operator node:** `tw up` joins mgmt/hostd to the tailnet by tsnet interactive login (browser URL), advertising `tag:tw-mgmt`/`tag:tw-host`; state persists, so later runs need no login.
2. **Developer ↔ mgmt:** mgmt serves its API on its own tsnet node and authenticates callers by **WhoIs** (`autogroup:admin` or app cap `github.com/rosskukulinski/tailworks/cap/admin` from a grant to `tag:tw-mgmt`; hosts by `tag:tw-host`). `tw link <mgmt>` makes every CLI command remote over the developer's own Tailscale connection; `deploy` uploads the locally built bundle; audit carries the WhoIs login.
3. **Minting:** WIF is the default mint mode: mgmt's OIDC issuer over Funnel, one-time trust credential in the admin console, only public client IDs stored. `--api-key-file` remains as labelled fallback mode.
4. macOS gets a LaunchAgent for `--service`.
**Honest limit:** creating *new* devices needs a control-plane credential; connection identity cannot do it. WIF is the keyless option, and the one-time console step remains until Tailscale offers a "connect a platform" flow and tag-scoped scopes (DX-fit asks #1 and #3).
