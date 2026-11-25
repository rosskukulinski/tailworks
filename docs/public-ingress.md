# Public ingress (Funnel)

Everything tailworks runs is tailnet-only by default: callers are identified by
their WireGuard identity (`getCaller`). Some apps need a public surface
too: a webhook receiver (GitHub, Stripe, Slack), an OAuth callback, a
public page. Tailworks can serve **chosen path prefixes** of a workload to the
public internet over [Tailscale Funnel](https://tailscale.com/kb/1223/funnel).
The design is ADR-029.

## How it works

- **One public node per public workload.** It is an extra node named after
  the workload, so the URL is `https://<workload>.<tailnet>.ts.net/`. It
  carries `tag:tw-public`, needs no grants, and listens **only** on
  Funnel (`:443`, TLS terminated on the node).
- **Every public request goes to the workload's Worker** (its default
  export), never straight to a Durable Object. Object nodes are never
  public. Your routing (`routeAgentRequest`, `/agents/<class>/<name>`)
  decides which object a request reaches.
- **The public node is netd's own listener.** There is no extra gateway
  hop, so `tw expose off` and `tw kill` act on it directly.

## Opting in

In `tailworks.jsonc`:

```jsonc
{
  "public": {
    // path patterns: "/x" (exact), "/x/*" (/x and below), "/*" (everything)
    "routes": ["/webhooks/*", "/oauth/callback"],
    // routes that may upgrade to WebSocket/SSE (default: none)
    "upgrade": []
    // optional: "limits" overrides the defaults, up to the ceilings (Limits, below)
  },
  "tailnet": {
    "objects": {
      // optional: let public requests create objects of this class
      "Room": { "public": { "create": true, "maxObjects": 100 } }
    }
  }
}
```

- **No `"public": true` shorthand.** If you mean every path, write `"/*"`.
- **Never public:** anything under `/_tw` (the platform's own object
  addressing), paths with dot segments or doubled slashes, encoded
  slashes or backslashes, and control characters (`%00`–`%1F`, `%7F`):
  these get `400 bad_request`.
- **Unlisted paths** get `404 not_public` from netd and never reach your
  code.
- **Your app receives the path decoded once** (`/%252e%252e/x` arrives as
  `/%2e%2e/x`), which is the form netd checked; don't decode it again.

### The tenant gate

Routes only take effect if the tenant allows public ingress:

```sh
tw tenant set <tenant> --public-ingress allow   # or deny
```

- **Single-tenant (`tw up`):** the gate is set to `allow` the first time,
  and an explicit `deny` is kept on later runs.
- **Gate is `deny`:** a deploy with `"public"` is refused with
  `public_ingress_not_allowed`. Switching the gate to `deny` later closes
  every public listener of the tenant.

### Tailnet policy

`tw deploy` checks this and prints it if it's missing:

```jsonc
"tagOwners": { "tag:tw-public": ["autogroup:admin", "tag:tw"] },
"nodeAttrs": [ { "target": ["tag:tw-public"], "attr": ["funnel"] } ]
```

- **HTTPS:** your tailnet needs HTTPS certificates (admin console › DNS ›
  HTTPS Certificates).
- **No new OAuth scopes:** `auth_keys` with tag `tag:tw` already lets tailworks
  mint `tag:tw-public` nodes.
- **tailworks never adds `funnel` to any other tag.** mgmt's node
  (`tag:tw-mgmt`) is never Funnel-capable (`internal/policy.Check`
  refuses it).

### Deploying

```sh
tw deploy                      # prints the public URL
tw deploy --wait-public 10m    # …and waits until it answers from the internet
```

- **The first fetch of a new name is slow:** the first certificate for a
  new name takes about 1–2 minutes, and public DNS can take up to
  10 minutes. Later deploys reuse the node and its certificate.
- **Name taken:** if another device already has the name, MagicDNS would
  append `-1`. Tailworks refuses to serve that name (`public_name_taken`).

### Name ownership

The public URL is `https://<workload>.<tailnet>.ts.net/`, so a public name
is shared by every tenant on a tailnet. mgmt gives each name one owner:

- **First deploy claims it.** The first tenant on a tailnet to deploy
  `"public"` routes under a workload name owns that name on that tailnet.
  The claim is made when the deploy is accepted, even if the deploy then
  fails to place.
- **The claim persists.** Undeploying, removing `"public"`, or killing the
  public node doesn't release the name. A webhook sender that still has the
  old URL can't end up sending signed payloads to another tenant's code.
- **Other tenants are refused.** A deploy or `--dry-run` by another tenant
  on the same tailnet that declares `"public"` under that name fails with
  `409 public_name_owned`, and so does a host's request to create that
  tenant's public node. A tailnet-only deploy of the same workload name is
  unaffected, and so are tenants on other tailnets.
- **Scope.** The scope is the tenant's `tailnet`, compared case-insensitively
  and without a trailing dot. Give every tenant on one tailnet the same
  tailnet name (its MagicDNS suffix): two tenants that name the same tailnet
  differently can't see each other's claims.
- **Releasing.** `tw expose names` lists the tenant's names.
  `tw expose release <name>` releases one once nothing serves it (else
  `409 public_name_in_use`: undeploy the workload, or remove `"public"` and
  redeploy, first). After a release any tenant on the tailnet can deploy
  public routes under the name. A team tenant's names carry its prefix,
  as its URLs do. Deleting a tenant releases all of its names. Claims and releases are audited (`public_name_claim`,
  `public_name_release`), and a refusal is a `deny` record with code
  `public_name_owned` whose detail names the owner.
- **Upgrading.** Names served before ownership existed have no claim yet.
  The first claim of such a name goes to the tenant serving it: the one
  with a live public node, or else the one with the earliest live public
  deployment. Whoever deploys first after the upgrade doesn't get it.

## What your code sees

```ts
import { getCaller, getRequester } from "@tailworks/tailnet";

export default {
  async fetch(req, env, ctx) {
    const who = getRequester(req);
    if (who.kind === "public") {
      // who.ip, who.proto ("https"), who.host — no identity
    } else {
      // who.kind === "tailnet": the full Caller (nodeId, tags, user, caps())
    }
  },
};
```

- **`getCaller(req)` stays tailnet-only.** On a public request it throws
  `TailnetError("public_request")`, so code that authorizes on the caller
  fails closed instead of treating "no identity" as a principal.
- **Public kind can't be forged.** netd sets it from the Funnel connection
  itself, never from a header. Client copies of every identity header are
  stripped: `Tailscale-*`, `X-Forwarded-*`, `CF-*`, `x-tw-*`,
  `Forwarded`, `X-Real-IP`.
- **Client IP:** netd sets `CF-Connecting-IP` and `X-Forwarded-For` (one
  value) to the client address Funnel reports, and `X-Forwarded-Proto:
  https`.
- **`request.url` is `https://`**, the URL the client called
  (`https://<workload>.<tailnet>.ts.net/…`), as on Cloudflare. Before
  [#360](https://github.com/rosskukulinski/tailworks/issues/360) a public
  request's `request.url` was `http://` with `X-Forwarded-Proto: https`.
  Tailnet requests keep `http://` unless the app sets
  [`tailnet.https`](tailworks-jsonc.md#tailnethttps). There is no geo data: `request.cf.country`, `colo`, `latitude`
  and the other geo fields are absent ([wrangler.md](wrangler.md#requestcf)).

## Objects and spawn amplification

In tailworks every new Durable Object name becomes a tailnet node, so public
traffic must not be able to mint nodes.

- **New objects are refused.** A public request can reach objects that
  already exist (created by tailnet traffic). Reaching a name that doesn't
  exist fails with `403 public_create_denied`, before the object is
  created.
- **No spawning.** `ctx.tailnet.spawn()` and `kill()` are refused in a
  public request, and so is `newUniqueId()`.
- **Opt-in creation.** A class may set `"public": {"create": true,
  "maxObjects": N}`.
  - **Caps:** N is required and at most 1000. Creations are also limited to
    20 per minute per tenant.
  - **Audit:** such objects are recorded with `actingFor: "public"`.

How the guard holds (see [docs/create-admission.md](create-admission.md)):

- **Creation is admitted, never implied.** hostd creates an object only
  when something admitted it: a tailnet request's create ticket, an object
  acting as itself, or `public.create` for a public request. Code that
  reaches the raw bindings some other way (`import { env } from
  "cloudflare:workers"`, an `env` saved in a global, the platform binding
  itself) can reach **existing** objects but gets `403 create_not_admitted`
  for new names, from the internet and from the tailnet alike.
- **Residual:** a tailnet request's ticket is readable by your code and
  valid for two minutes; code that saves it (or the guarded env) in a
  global lets a public request in that window create through it. Don't
  cache `env`.
- **Chained creation:** an object a public request may reach can still
  create other objects in its own code. That is your app's logic.
- **`"node": false` classes:** they mint no node, so no admission applies;
  they are reachable from public requests only with `"public": {"create":
  true}`.

## Limits

netd enforces these on the public listener. Each is overridable in
`public.limits`, up to a ceiling.

| Limit | Default | Ceiling | Over the limit |
|---|---|---|---|
| `rps` / `burst` (per workload) | 50 / 100 | 1000 / 2000 | `429 rate_limited` + `Retry-After` |
| `ipRps` / `ipBurst` (per client IP) | 10 / 20 | 200 / 400 | `429 rate_limited` + `Retry-After` |
| `maxBodyBytes` | 1 MiB | 32 MiB | `413 payload_too_large` |
| `maxHeaderBytes` | 32 KiB | 64 KiB | `431 header_too_large` |
| `timeoutSeconds` | 30 | 300 | `504 request_timeout` |
| `maxConns` (per workload) | 256 | 4096 | connection closed |
| `maxConnsPerIp` (per client address) | 32 | 1024 | connection closed |
| `upgradeLifetimeSeconds` | 3600 | 86400 | upgraded connection closed |

`burst` and `ipBurst` are spike sizes; sustained traffic is admitted at
`rps` and `ipRps`. When you raise `rps` (or `ipRps`) and leave its burst
unset, the burst is two seconds' worth of the rate, never below the
default and never above the ceiling: `"rps": 200` gets a burst of 400.

Upgrades on a route not listed in `upgrade` get `403 upgrade_not_allowed`.

Connections:

- **Per client:** one address can hold at most `maxConnsPerIp` of the
  workload's `maxConns` connections, so a single client can't fill them
  and lock everyone else out. An IPv6 client counts per /64. Clients
  behind one NAT share the cap, so raise it if many of your users share
  an address.
- **First request:** a new connection has 5 seconds to finish its TLS
  handshake and send its first request's headers, or it's closed. After
  that, an idle keep-alive connection is closed after 60 seconds, and
  each later request has 10 seconds to send its headers.
- **Upgrades:** an upgraded (WebSocket) connection is closed after
  `upgradeLifetimeSeconds`, even if it's busy. Clients should reconnect.
  `timeoutSeconds` doesn't apply to upgrades.
Funnel has its own bandwidth limits, which Tailscale doesn't publish.

**Error bodies.** netd's refusals are the typed JSON error (`code`,
`detail`, `doc_url`, and the `tw-error` header). A request whose `Accept`
prefers `text/html` to JSON (a browser) gets a minimal HTML page instead,
with the same status and `tw-error` header: the status, the code, one
fixed sentence per code, `Retry-After` when there is one, and the docs
link. The page never includes the request's path, the detail or anything
about the workload. A wildcard `Accept: */*` (curl) or a tie keeps JSON.
Errors your own code returns are passed through unchanged.

## Exposing and unexposing

```sh
tw expose off <workload>    # close the public listener now; survives redeploys
tw expose on  <workload>
tw expose names             # the public names this tenant owns
tw expose release <name>    # give one up, once nothing serves it
```

- **`off`:** closes the Funnel listener and resets in-flight public
  connections. The node stays joined, so its certificate stays warm.
  Measured on a real tailnet, see STATUS.md.
- **Removing `"public"` from `tailworks.jsonc` and redeploying** deletes the
  public node.

## Audit

- **Per request:** every public request that matched a route is a
  `public_request` record in the host audit chain
  (`tw audit <public-object-id>`). It holds the method, the path
  **without the query string**, the matched route, the status, the client
  IP, bytes and duration.
- **Refusals:** requests the limits refused (429/413/431, and connections
  over `maxConns` or `maxConnsPerIp`) and paths refused before routing
  (`404 not_public`, `400 bad_request`) are rolled up in one
  `public_rejected` summary per minute, so a refused flood or a path scan
  can't grow the chain. The summary has `rateLimited`, `tooLarge`,
  `connsRefused`, `ipConnsRefused`, `notPublic`, `badRequest` and the top
  client IPs. The limit refusals also go to the `tw.public.rejected`
  metric (see [observability](observability.md)).
- **Backlog:** while mgmt is unreachable, hostd queues host records in
  memory and then on disk; none is dropped. If the disk queue reaches its
  cap, further `public_request` records are refused and counted. Every
  refused record is reported in an `audit_dropped` record, so the chain
  still verifies and the gap is visible.
- **Other records:** `public_object_created`, and `expose_on`/`expose_off`.
  The operator's change is recorded on mgmt's chain and the listener
  change on the host's.

## Under faults

The data-plane chaos loop (`e2e/dataplane:TestRouting`, scenarios
`public` and `routing`) drives netd's public listener directly while it
moves the workload, takes it over from a stalled or cut-off host, restarts
hosts and mgmt, and changes the route list under load:

- **Only the lease holder answers.** A public request is never answered
  by a copy whose lease has moved on, and every public request reaches
  the Worker, which sees a public requester.
- **Route changes.** A path that isn't listed never reaches your code: a
  redeploy that removes a route takes effect when the new deployment is
  active, and the loop checks a 10 s bound. A tailnet-only path over the
  public listener is always `404 not_public`.
- **Availability.** While a host is stalled or cut off, the public URL
  fails until the takeover (about 45 s, docs/locks.md). The loop reports
  each path's availability in and out of fault windows.

## Checking it

`tw doctor` shows:

- `https_certs`;
- `public_ingress` (the tenant gate);
- per public workload, `funnel_attr:<workload>` (needs a readable
  policy);
- per public workload, `public_probe:<workload>`: public DNS plus a TLS
  handshake from this machine, with the certificate's expiry.

## Not in v1

- **Signing in tailnet users on public routes (tsidp).** tsidp refuses
  `/authorize` over Funnel and refuses tagged nodes. A later ADR covers it.
  An OAuth client whose user signs in from a tailnet device (a hosted
  assistant's MCP connector) works today:
  [mcp-public.md](mcp-public.md).
- **Others:** custom domains (Funnel serves `*.ts.net` only), a
  bring-your-own front door (Caddy, Cloudflare Tunnel), GeoIP, the Node
  process tier (`"runtime": "node"`), and inbound email or cron triggers.
  For email, use a provider's inbound webhook pointed at a public route.
