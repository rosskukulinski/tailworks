# Running tailworks on Headscale

tailworks runs on a self-hosted [Headscale](https://github.com/juanfont/headscale)
coordination server as well as on Tailscale. This is the **Headscale
profile** ([ADR-015](design/adr/015-headscale-profile-framework-scope.md),
RFC §7.2). Tailworks needs no changes to Headscale itself.

Needs Headscale **v0.29 or later**: tailworks' caps travel in policy `grants`,
which v0.28 and earlier reject. Tailworks checks the version up front and refuses
an older server with `profile_unsupported`. Tested against v0.29.7 with
`make e2e-headscale`, which runs a pinned release in Docker. The RFC's
"latest two minor versions" promise applies from v0.30 on.

## What's different from Tailscale

| | Tailscale | Headscale profile |
|---|---|---|
| Minting node keys | OAuth client | A Headscale **API key** mints tagged, single-use pre-auth keys through Headscale's REST API (`/api/v1`) |
| Long-lived secret | none with WIF | **yes**: the API key. Keep its file `0600` and rotate it (below) |
| Workload address | Tailscale Service `svc:<name>` (`tailnet.service`) | The workload's own node: `<workload node>.<base_domain>` (MagicDNS). `tailnet.service` is refused with `profile_unsupported` |
| Public routes (Funnel) | `tailworks.jsonc` `public`, `tw expose` | Not available: Headscale has no Funnel. `publicIngress` is always `deny` |
| Kill switch | Tier 1 (netd), Tier 2 (device delete), Tier 3 (posture) | Tiers 1 and 2. Tier 2 deletes the Headscale node |
| Caps (`app`) | in grants | in grants (the same). `nodeAttrs` carry no `app` |
| HTTPS on mgmt's node | when the tailnet has HTTPS certificates | plain HTTP on `:80` (WireGuard-encrypted, WhoIs-authenticated) |
| `tailnet.https` (a workload on `https://`) | when the tailnet has HTTPS certificates | not available: Headscale issues no ts.net certificates, so the deploy fails with `tailnet_https_unavailable` |
| Policy owners | `autogroup:admin`, `tag:tw` | `tag:tw` only. Headscale rejects `autogroup:admin` |
| Caller grant (`--apply-policy` on a new app) | `autogroup:member` → the app's tag | the same: Headscale accepts `autogroup:member` as a grant source. `autogroup:admin` and other autogroups are left out |

Everything else works the same way: Durable Objects with their own nodes,
WhoIs callers (`getCaller`), spawn narrowing, the pre-joined node pool,
audit and deploy preflight. (On-behalf-of assertions aren't built on either
profile: RFC §4.6.)

## Set up Headscale

You need a Headscale server with:

- `dns.magic_dns: true` and a `dns.base_domain` (for example
  `hs.example.net`). Tailworks treats the base domain as the tailnet name.
- `policy.mode: database`, if you want `--apply-policy` to write tailworks'
  policy. With `policy.mode: file`, tailworks prints the snippet and you add it to
  the file yourself.

Create an API key on the Headscale server and put it in a `0600` file.
`~/.config/tailworks/headscale.env` is where `tw up` looks for it by
default:

```sh
headscale apikeys create --expiration 90d
umask 077; echo 'HEADSCALE_API_KEY=<the key>' > ~/.config/tailworks/headscale.env
```

## Start tailworks

```sh
tw up --control-url https://headscale.example.net --tailnet hs.example.net
```

A `--control-url` other than Tailscale's selects the Headscale profile when
there is an API key: `--api-key-file <file>`, or, on the default state dir
(`~/.tailworks`), `~/.config/tailworks/headscale.env` when it exists.
`tw up` never asks for the key or creates that file. `--control=headscale`
is also accepted. `--tailnet` is the server's `dns.base_domain`: an API key
doesn't name it.

`--control-url` is Headscale's `server_url`. Its REST API is served at the
same URL. `tw up` checks the key against the server, then writes the
profile's policy snippet (validated by Headscale's own parser first). After
that it mints mgmt's node key and joins.

**Headscale refuses to register a node whose tags its policy doesn't
define.** So on this profile the policy goes in *before* anything joins.
On a terminal, `tw up` shows the snippet and asks before writing it (when
Headscale's parser accepts the merged policy). Without a terminal, use
`--apply-policy --yes`. If you answer `n`, or run it without a terminal
and without `--apply-policy`, or with `--apply-policy=false`, `tw up`
stops with `policy_missing_tag_owners` and prints the snippet:

```hujson
"tagOwners": {
  "tag:tw": [],
  "tag:tw-host": ["tag:tw"],
  "tag:tw-mgmt": ["tag:tw"],
},
"grants": [
  {"src":["tag:tw-host"],"dst":["tag:tw-mgmt"],"ip":["tcp:443","tcp:80"]},
  {"src":["tag:tw-mgmt"],"dst":["tag:tw-host"],"ip":["tcp:7474"]},
],
```

`tag:tw` has no owners. Only the API key mints it, and no user can tag
their own device with it.

An existing tw host can also serve a Headscale tailnet as a second tenant:

```sh
tw tenant add hs.example.net \
  --control-url https://headscale.example.net --api-key-file ~/.config/tailworks/headscale.env
```

That tailnet's policy needs only `"tag:tw": []` in `tagOwners` (its nodes
are workloads; tailworks' own nodes stay on the operator's tailnet).
`tw tenant add` prints the line when it is missing, and `--apply-policy`
writes it after asking.

## Deploy

Deploying works as usual. Each tag in `tailworks.jsonc` must be in Headscale's
`tagOwners` (owned by `tag:tw`); deploy preflight prints any that are
missing. Reach the workload at the MagicDNS name `tw deploy` prints
(`endpoint.magicDNS`). Grants work as on Tailscale, including `app` caps:

```hujson
"tagOwners": { "tag:tw-wl-hello": ["tag:tw"] },
"grants": [
  { "src": ["group:dev"], "dst": ["tag:tw-wl-hello"], "ip": ["tcp:80"] },
],
```

Don't set `tailnet.service` or `public` in `tailworks.jsonc` on this profile.
Both need Tailscale features (Services, Funnel) that Headscale lacks, and
deploy refuses them with `profile_unsupported`.

## Security notes

- **The API key is a full Headscale admin credential.** Headscale doesn't
  check `tagOwners` when the API mints a key, so the key can mint any tag,
  delete any node and rewrite the policy. Tailworks reads it from the file at
  each use and never copies or logs it. Keep it on the mgmt host only, mode
  `0600`.
- **Rotate the key.** Create a new one, write it to the same file (tailworks
  re-reads the file, so no restart is needed), then expire the old one:
  `headscale apikeys expire --prefix <old prefix>`. A short `--expiration`
  with a scheduled rotation limits the damage from a leaked key.
- Object keys are single-use and expire after 5 minutes. A killed object's
  node is deleted from Headscale (Tier 2). Lineage lives only in tailworks'
  audit log (`tw audit`).

## Testing the profile

`make e2e-headscale` starts the pinned Headscale release in Docker (pick
another with `HEADSCALE_IMAGE=headscale/headscale:<tag>`) and runs:

- the policy conformance check: the profile's policy is accepted, and what
  it leaves out (`autogroup:admin` as an owner or a source, `funnel`,
  `svc:`, postures, `app` in `nodeAttrs`) really is rejected by that
  release, and the caller grant from `autogroup:member` is accepted;
- the node lifecycle: mint, join, single-use, node-side rename (the node
  pool), delete;
- the version gate: v0.28.0 is refused before anything is minted;
- `tw up --control=headscale` end to end, in both process layouts: deploy,
  a call from another node with a verified caller, the `tailnet.service`
  refusal, kill and `tw down`.

To run it against your own server instead, set `TW_HEADSCALE_URL`,
`TW_HEADSCALE_API_KEY_FILE` and `TW_HEADSCALE_BASE_DOMAIN` (it needs
`policy.mode: database`, and it writes the policy).
