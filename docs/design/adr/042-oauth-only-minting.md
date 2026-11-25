# ADR-042: Mint node keys with an OAuth client only; remove the API-key and WIF mint modes

**Date:** 2026-11-15
**Status:** Accepted (Ross). Supersedes the minting parts of ADR-013 and ADR-026
(decision 3). ADR-026's decisions 1, 2 and 4 (tailnet identity for every
tailworks connection, WhoIs auth, `tw link`, the LaunchAgent) stand.

**Context.**

- tailworks mints a Tailscale auth key for every node it runs: mgmt's own node,
  each host's node, and every object. It had three ways to get the
  credential for that:
  - `oauth` (M12): a Tailscale OAuth client scoped to `auth_keys` for
    `tag:tw`, its secret in a 0600 file on the mgmt host. This is the
    mechanism the Tailscale Kubernetes operator and GitHub Action use.
    It has been the documented default since M12.
  - `wif` (ADR-026, M10): workload identity federation. mgmt is an OIDC
    issuer, a trust credential in the admin console trusts it, and every
    mint is a token exchange. No secret exists anywhere. The cost: a
    browser login for mgmt's node, a second node (`tag:tw-issuer`) that
    serves the issuer over Funnel, HTTPS certificates on the tailnet, a
    shared signing key in custody, a console step per tenant, and failure
    modes of its own (the issuer must be reachable from the internet, the
    certificate for a new node name takes minutes, and the issuer and
    subject must match the credential exactly).
  - `apikey`: a Tailscale API access token in a file. It can do anything
    its owner can and expires within 90 days. It was kept as an explicit
    fallback and for tests.
- WIF's one advantage over an OAuth client is that no secret exists. That
  matters when mgmt holds credentials for tailnets it doesn't belong to
  (RFC §4.4's hosted case). Tailworks is self-hosted (ADR-002): the operator owns
  the tailnet, the host and the 0600 file, so the OAuth client gives the
  same scoping (`auth_keys`, `tag:tw`) without the issuer, the second
  node, Funnel or HTTPS.
- An API key has no advantage over an OAuth client for an operator. Its
  remaining users were test harnesses.
- Three modes meant three onboarding paths in the docs, three branches in
  `tw up`, `tw doctor` and the broker, and an e2e suite each.

**Decision.**

1. **Tailscale tenants mint only with an OAuth client** (mint mode
   `oauth`; scope Auth Keys write with tag `tag:tw`, optionally Devices
   Core, Policy File read and Services write). The Headscale profile is
   unchanged (ADR-015): it mints with the Headscale API key, mode
   `headscale`. `static` stays for `tw dev` and fakemgmt.
2. **Removed:**
   - mint modes `apikey` and `wif`;
   - `tw up --api-key-file` on Tailscale (the flag remains for
     `--control=headscale`; since 2026-11, `--api-key-file` also implies
     the Headscale profile when `--control-url` names a Headscale server,
     #539), `--wif`, `--wif-client-id`,
     `--wif-lifecycle-client-id`, `--issuer-authkey-file` and
     `--login-timeout`, and the browser-login branch of `tw up`;
   - `tw tenant add --api-key-file` on Tailscale (it takes
     `--oauth-client-file`), `tw tenant set`'s `--wif-client-id` and
     `--wif-lifecycle-client-id`, `tw doctor`'s `--tailnet` and `--api-key-file`;
   - mgmt's OIDC issuer: the discovery and JWKS endpoints, the issuer
     signing key, `tw-mgmt -issuer-url`, `-issuer-listen`, `-funnel` and
     `-issuer-tsnet-*`, and the `tag:tw-issuer` node; `tag:tw-issuer` is
     no longer an allowed Funnel target in `internal/policy`;
   - `POST /v1/tenants/{id}/wif-check` and the tenant fields
     `wifClientID`, `wifLifecycleClientID` and `wif`;
   - `tw-netd -mint-api-key-file` (now `-mint-oauth-client-file`).
3. **Existing installs keep their data and switch explicitly.**
   - mgmt refuses to create a tenant in a removed mode, or to switch one to
     it, with `400 mint_mode_removed`. A tenant an older tailworks stored in one
     keeps its row, its deployments and its running nodes, but every mint
     fails with `mint_mode_removed` until it is switched.
   - `MintMode` keeps `apikey` and `wif` as values mgmt may report (for
     those rows) but never accepts.
   - Switching is a PATCH to `{mintMode: oauth, oauthClientFile: …}`:
     `tw up --oauth-client-file <file>` on a state dir in a removed mode
     does it for the operator tenant, and `tw tenant set <tenant>
     --oauth-client-file <file>` for an added one. `tw doctor` fails its
     `mint_mode` row until then.
   - The store's `wif_client_id` and `wif_lifecycle_client_id` columns stay
     (no migration). mgmt no longer reads them and clears them on update.
4. **Tests.** Hermetic fakes serve `/api/v2/oauth/token`. Real-tailnet
   suites create an OAuth client per run with the harness's own API key
   (`cliflow.RealOAuthClient`) and delete it afterwards. The API key
   remains the harness's tool for policy setup, test nodes and cleanup,
   and never reaches tailworks.

**Consequences.**

- One credential path to document, implement and test. `tw up` without
  a credential explains how to create the OAuth client instead of
  offering a choice.
- mgmt serves nothing over Funnel and needs no HTTPS certificates; its
  node is the only infrastructure node a single host runs. There is no
  issuer signing key in custody.
- A secret exists: the OAuth client's, in a 0600 file on the mgmt host,
  re-read at each exchange and never logged or copied. Revoking the
  client in the console stops all future minting.
- Installs set up in `apikey` or `wif` mode need a one-time switch
  (docs/INSTALL.md, "Upgrading from API-key or WIF mode"). After a WIF
  install switches, its `tw-issuer` node, trust credentials and
  `tag:tw-issuer` policy entries are left for the operator to remove.
- A mgmt that holds credentials for other operators' tailnets would want
  a secretless credential again. WIF can come back then, as its own
  decision, behind the same broker interface.
