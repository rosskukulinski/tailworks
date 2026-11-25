Last update: 2026-11-15 PT. Removed the `apikey` and `wif` mint modes: Tailscale tenants mint with an OAuth client only (ADR-042, Proposed).

## What changed

- **Mint modes.** `oauth` (Tailscale) and `headscale` (ADR-015) remain;
  `static` stays for `twd dev` and fakemgmt. mgmt refuses `apikey` and
  `wif` on create and PATCH with `400 mint_mode_removed`
  (`mgmt/mintmodes.go`); a tenant an older twd stored in one fails every
  mint with the same code and is switched by a PATCH to
  `{mintMode: oauth, oauthClientFile}`. `MintMode` keeps both values in the
  spec as report-only. The store's `wif_*` columns stay; mgmt no longer
  reads them and clears them on update (no migration).
- **Removed from mgmt:** the OIDC issuer (`mgmt/issuer.go`, the shared
  issuer key, discovery/JWKS routes), `twd-mgmt -issuer-url`,
  `-issuer-listen`, `-funnel`, `-issuer-tsnet-*`, the issuer's Funnel node,
  `POST /v1/tenants/{id}/wif-check`, the `wif*` tenant fields, the WIF and
  API-key branches of the broker. `tsapi.ExchangeToken` and
  `CreateFederated` are gone; `tag:twd-issuer` is no longer a Funnel target
  in `internal/policy`.
- **CLI.** `twd up`: `--oauth-client-file` or, with `--control=headscale`,
  `--api-key-file`; `--api-key-file` on Tailscale is `mint_mode_removed`;
  `--wif*`, `--issuer-authkey-file`, `--login-timeout` and the browser
  login branch are gone. A state dir in a removed mode refuses
  (`mint_mode_removed`) until `twd up --oauth-client-file` switches it
  (tenant PATCHed, mgmt's node and deployments kept). `twd tenant add
  --oauth-client-file`, `twd tenant set --oauth-client-file` (new).
  `twd doctor`: no `--tailnet/--api-key-file`; `mint_mode` fails on a
  removed mode; `api_key` is Headscale-only. `twd-netd
  -mint-api-key-file` became `-mint-oauth-client-file`.
- **Test harness.** Hermetic fakes serve `/api/v2/oauth/token`;
  `cliflow.WriteFakeOAuthFile` is the shared fake client file. fakemgmt's
  `apikey` mode became `oauth`. Real-tailnet suites create a run-scoped
  OAuth client with the harness API key (`cliflow.RealOAuthClient`) and
  delete it at cleanup. Deleted: `e2e/keyless`, `e2e/real/mgmt/wif_real_test.go`,
  `make e2e-keyless`.
- **Docs.** INSTALL (one credential path; new "Upgrading from API-key or
  WIF mode"), errors.md (`mint_mode_removed` added; `https_required`,
  `issuer_unreachable`, `wif_credential_required`, `wif_exchange_failed`
  removed; `funnel_required`, `login_timeout`, `policy_unavailable`,
  `tailnet_required` reworded), README, RFC §4.4 and related rows, the
  threat model, ADR `docs/design/adr/042-oauth-only-minting.md`, ADR-013
  and ADR-026 marked superseded in part. Release note:
  `changes/remove-wif-apikey.md` (kind breaking).

## Tests

Final, after merging `origin/master` (through #87):

- `make lint test`: PASS (Go `-race` over every hermetic package, JS).
  The merge brought `mgmt/rt3_publicname_test.go` (#82) calling the old
  `newTestEnv(t, nil)`; updated to `newTestEnv(t)`.
- `make test-pg` (Postgres via OrbStack): PASS.
- `make e2e-upgrade`: PASS (`TestUpgradePreviousToCandidate`).

Earlier on this branch:

- `make lint`: PASS.
- `make test` (Go `-race` over every hermetic package, JS): PASS after one
  fix (the docs policy-snippet count went from 3 to 2 with the WIF snippet
  removed). `TestTemplateTests` and `TestDoctor` fail before `make deps`
  on a machine with stale JS builds or an old workerd; `make test` runs
  deps first.
- New: `mgmt.TestRemovedMintModes` (create/PATCH refusals, a stored
  `apikey` row fails to mint with `mint_mode_removed`, stays patchable,
  mints after the switch to oauth); CLI checks that `twd up
  --api-key-file` on Tailscale is `mint_mode_removed`.
- Real tailnet (tailnet 1), with run-scoped OAuth clients:
  - `make e2e-mac`: PASS (`TestE2EMac`, 77 s).
  - `make e2e-real`: PASS (`TestRealHostd`, `TestRealTailnet`,
    `TestMgmtRealMintJoinKill`), 0 devices left.
  - The first `e2e-real` run failed: an OAuth client tagged `tag:ttest`
    still can't mint `tag:ttest` keys (`400 requested tags [tag:ttest] are
    invalid or not permitted`); a client mints only tags its own tag owns.
    `RealOAuthClient` now makes `tag:twd` an owner of the suite's object
    tags in the tailnet policy (additive, validated, If-Match) instead.
    This changed tailnet 1's policy: `tag:ttest` is now also owned by
    `tag:twd`.

## Not run

- `make e2e-oauth`, `e2e-multi`, `e2e-svc`, `e2e-public`, `e2e-linux`,
  `e2e-linuxvm`, `e2e-mbp`, coldid real: they compile (`make lint` vets
  every tag) but were not run. `e2e-multi` needs tailnet 2 to let
  `tag:twd` own its test tags too; coldid real and `e2e-real` use the same
  `RealOAuthClient(…, "tag:ttest")` path that passed above.

## Follow-ups

- Number the ADR at merge (`git grep ADR-042`).
- twd-mgmt could log a warning at start for tenants still in a removed
  mode; today the error appears at the first mint, in `twd doctor` and in
  `twd up`.
