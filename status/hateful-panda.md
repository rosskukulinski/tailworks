Last update: 2026-11-15 12:30 PT. ADR-015 Headscale profile built; e2e green against real Headscale v0.29.7 (both process layouts).

# Headscale profile (ADR-015, RFC §7.2)

## What's built

- **`internal/headscale`**: a Headscale REST client (`/api/v1`) shaped like
  `tsapi.Client`:
  - `CreateKey` mints a tagged, user-less, single-use pre-auth key;
  - `DeleteKey` expires a key;
  - `DeleteDevice` is `DELETE /node/{id}`;
  - `ListDevices`;
  - the policy calls get, check and put, with a content-hash ETag and an
    If-Match emulated by re-reading;
  - `CheckVersion` refuses servers older than v0.29.

  Errors are `*tsapi.APIError` (with `API: "headscale api"`), so the
  `tsapi.Is*` helpers work unchanged.
- **mgmt** (`mgmt/headscale.go`, plus one-line hooks in shared files):
  - New mint mode `headscale`. It needs `controlURL` (the Headscale
    `server_url`, where the API is too) and `apiKeyFile`
    (`HEADSCALE_API_KEY=…`, 0600, re-read at each use).
  - `Broker.API` returns the Headscale client for every purpose.
  - Refusals, all with the new error code `profile_unsupported`:
    `tailnet.service` at deploy and preflight; public routes;
    `publicIngress: allow` at create and patch. `PublicAllowed()` is always
    false on this profile.
  - The preflight's tagOwners snippet uses `["tag:twd"]`, not
    `autogroup:admin`.
- **policy** (`internal/policy/headscale.go`): `policy.Headscale(snippet)`
  drops `autogroup:*` owners and sources and all nodeAttrs. `tag:twd` gets
  no owners; twd's other tags are owned by `tag:twd`.
  `Check(..., Options{Headscale: true})` rejects what Headscale's parser
  would.
- **CLI** (`cmd/twd/headscale.go`):
  - `twd up --control=headscale` and `twd tenant add --control=headscale`.
    `--control-url` must name the Headscale server.
  - `--oauth-client-file` and `--wif` are refused on this profile.
  - The policy is applied before mgmt's key is minted (see the findings).
  - `twd doctor` checks the Headscale key.
  - `twd host add` prints `--key-tags-only` for Headscale hosts.
- **Nodes advertise no tags on Headscale**: `netd.Config.KeyTagsOnly`, set
  by hostd from the tenant's mint mode; `hostdrun.Options.TSNetKeyTagsOnly`;
  `twd-hostd -tsnet-key-tags-only` and `join --key-tags-only`; and an empty
  `TSNetTags` for mgmt's node.
- **API**: `MintMode` gains `headscale`, listed one per line. `ErrorCode`
  gains `profile_unsupported`. Regenerated with `make api`.
- **Docs**:
  - new: `docs/headscale.md`;
  - pointers: `docs/INSTALL.md`, `README.md`, `llms.txt`;
  - `docs/errors.md#profile_unsupported`;
  - notes added to ADR-015 and RFC §7.2.

## Tests

- Unit (hermetic, run by `make test`):
  - `go test ./internal/headscale` covers the fake gateway, key and node
    mapping, ETag/412 and the version gate;
  - `go test ./internal/policy -run Headscale`;
  - `go test ./mgmt -run TestHeadscaleProfileTenant` covers mint through
    the tenant's Headscale, kill Tier 2 deleting node `12`, and the
    Service, public and preflight refusals.
- `make e2e-headscale` (Docker, `headscale/headscale:v0.29.7`):
  - `TestPolicyProfile`: the profile policy is applied, idempotent, keeps
    operator comments, and a stale ETag gets 412. The real server rejects
    `autogroup:admin` owners, `funnel`, `svc:` destinations,
    `autoApprovers.services`, `postures`, `srcPosture` and `app` in
    `nodeAttrs`.
  - `TestNodeLifecycle`: an expired key can't join, and a key can't be
    used twice. The node joined in 2.1 s, with StableNodeID = `1` (decimal)
    and the key's tags. A node-side rename reached MagicDNS in 1.23 s.
    `DeleteDevice` works, and repeating it is a no-op.
  - `TestTwdUp/single`: `twd up` took 4.2 s, deploy 2.2 s. A `tag:ttest`
    client got a 200 with a verified caller. `tailnet.service` was refused.
    Kill removed the Headscale node 1.6 s later.
  - `TestTwdUp/separate-host`: the same flow, with `twd up` taking 12.8 s.
  - `TestRefusesOldHeadscale`: v0.28.0 is refused with
    `profile_unsupported` before any node joins.
- `make lint` and `go test ./cmd/twd` pass.

## Findings (real Headscale v0.29.7)

1. **v0.28 and earlier reject `grants`** ("unknown field"), so the profile
   needs v0.29 or later (where caps travel in grants only). The RFC's
   "latest two minors" promise can apply only from v0.30.
2. **A pre-auth-key registration that advertises tags is refused**
   (`requested tags [...] are invalid or not permitted`, `state.go`: "PreAuthKey
   nodes get their tags from the key itself"). That is why every twd node
   advertises no tags on this profile.
3. **A tag must be defined in the policy before a node with it registers.**
   `twd up` therefore applies the snippet up front. Without
   `--policy-apply` it stops with `policy_missing_tag_owners` before
   minting.
4. **`autogroup:admin` is rejected as an owner.** `tagOwners: {"tag:twd": []}`
   is accepted.
5. **The API mints any `tag:` whatever `tagOwners` says.** The API key is
   the whole trust boundary (documented).
6. An empty database-mode policy returns `500 "acl policy not found"`; the
   client maps it to `{}`.

## Gaps / next

- The Tailscale e2e suites (`e2e-real`, `e2e-oauth`, `e2e-multi`, …) were
  not run: no Tailscale credentials in this worktree. The Tailscale code
  path changed only where the profile is checked. Tag advertisement is
  unchanged unless the mint mode is `headscale`.
- `twd link` over a Headscale operator tailnet is not covered. It needs an
  admin grant from a user or group; `autogroup:admin` doesn't exist there.
- Agents with spawned sub-objects on Headscale are not exercised end to end;
  only the workload node is. Node lifecycle and pool renames are covered
  separately.
- The ADR-015 framework-scope half (Agents SDK and MCP adapters) is
  unchanged by this branch.
- No CI workflow runs `make e2e-headscale` yet; the repo has only
  `release.yml`.
