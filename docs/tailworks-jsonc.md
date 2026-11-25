# tailworks.jsonc reference

A tailworks app has a `tailworks.jsonc` next to its wrangler config (`wrangler.jsonc`,
`wrangler.json` or `wrangler.toml`; [wrangler.md](wrangler.md)). The split:

- **The wrangler config** is the Worker itself, as on Cloudflare: `main`,
  `compatibility_date`, `durable_objects` bindings, `migrations`, `vars`,
  `triggers.crons`, `queues` ([queues.md](queues.md)), `containers`,
  `d1_databases` ([d1.md](d1.md)), and `env.<name>` environments
  (`tw deploy --env`).
- **`tailworks.jsonc`** is what tailworks adds: the tailnet identity of the workload and
  of each Durable Object class, the isolation tier, placement, and public
  routes.

`tailworks.jsonc` is JSONC: comments and trailing commas are fine.

A project with only a wrangler config deploys too. Then tailworks uses defaults:
the workload node is tagged `tag:tw-wl-<workload>` (the same default as a
`tailworks.jsonc` without `tailnet.tags`), and every Durable
Object class is internal (`"node": false`). `tw deploy` says when it does
this ([wrangler.md](wrangler.md#without-a-twjsonc)).

## Validation and editor completion

`tw dev` and `tw deploy` check `tailworks.jsonc` against
[`schema/tailworks.schema.json`](../schema/tailworks.schema.json) before doing anything
else. An unknown key or a bad value stops the command:

```
error [bad_request]: tailworks.jsonc has 1 problem
  tailnet.objcts: unknown key (did you mean "objects"?)
```

This matters most for `tailnet.objects`. If that key is misspelt, every
class deploys as an ordinary Durable Object with no node of its own, and
nothing else would tell you.

For completion and inline errors in your editor, point `$schema` at the
schema. `tw init` writes this line for you:

```jsonc
{
  "$schema": "https://raw.githubusercontent.com/rosskukulinski/tailworks/master/schema/tailworks.schema.json",
  "name": "support",
  ...
}
```

## Top-level keys

| key | type | default | what it does |
|---|---|---|---|
| `$schema` | string | | the schema, for your editor; tailworks ignores it |
| `name` | string | wrangler `name`, then the directory name | the workload name, a DNS label. `tw deploy --name` overrides it. Leave it out when the wrangler config has a `name` (the templates do); when both set one and they differ, this one wins and `tw` says so |
| `runtime` | `"workerd"` \| `"node"` | `"workerd"` | `"node"` runs a Node.js app on the Node runtime ([node-tier.md](node-tier.md)); then there is no `wrangler.jsonc` |
| `main` | string | | `runtime: "node"` only: the entry file, e.g. `"src/server.ts"` |
| `node` | object | | `runtime: "node"` only: `env` (string map), `listen`, `health`, `args` ([node-tier.md](node-tier.md)) |
| `tier` | `"process"` \| `"microvm"` | the tenant's tier | `"microvm"` runs the workload in a Firecracker VM, on a Linux host with KVM ([INSTALL.md](INSTALL.md#linux-the-microvm-tier-firecracker-kvm)) |
| `replicas` | integer ≥ 1 | 1 | run a stateless workload on this many hosts ([services.md](services.md)) |
| `placement` | object | | which host runs the workload; [below](#placement) |
| `public` | object | | paths reachable from the public internet over Funnel; [below](#public) |
| `containers` | array or object | | container classes ([containers.md](containers.md)): wrangler's array form, or per-class image refs you pin; mgmt records the digests it resolves in the deployment |
| `tailnet` | object | | the workload's tailnet identity; [below](#tailnet) |

## `tailnet`

| key | type | what it does |
|---|---|---|
| `tags` | `["tag:…"]` | Tags of the workload's own node. Default (filled in by `tw deploy` and `tw dev`): `["tag:tw-wl-<workload>"]`, so a minimal `tailworks.jsonc` is `{}`. An empty `[]` is an error. Your policy must let `tag:tw` own them; `tw policy` (and `tw deploy`'s preflight) prints the snippet if it doesn't, and `tw policy --apply` writes it ([below](#tags-and-tw-init)) |
| `connects` | `["tag:…" or name]` | what the workload calls. `tw deploy` preflights these grants, and smart placement measures paths to them. Add the TCP port a socket dials (`"tag:db:5432"`) and the preflight checks the grant for that port instead of `tcp:80` |
| `service` | `true` or `"svc:<name>"` | a Tailscale Service: a stable name and VIP in front of the workload ([services.md](services.md)) |
| `https` | boolean | also serve the workload on `https://<name>.<tailnet>.ts.net` with its ts.net certificate, so `request.url` is `https://` for requests that arrive that way; [below](#tailnethttps) |
| `egress.internet` | `true`, `false` or `[host, "*.suffix", IP, CIDR]`, each with an optional `:port` | internet egress for Worker code and containers; [below](#tailnetegressinternet) |
| `egress.via` | `"exit-node"` | require a host whose internet egress leaves through a tailnet exit node; [below](#tailnetegressvia) |
| `spawn.maxLive` | integer | the most live sub-agents (`ctx.tailnet.spawn`) under one root object. Default and ceiling 64: a deployment may only lower it; a larger value is lowered to 64, and 0 or absent means 64 ([limits.md](limits.md#durable-objects)) |
| `spawn.maxDepth` | integer | the deepest spawn chain. Default and ceiling 3: a deployment may only lower it; a larger value is lowered to 3, and 0 or absent means 3 |
| `objects` | map of class → [object](#tailnetobjectsclass) | the Durable Object classes that get a tailnet node per instance |

### `tailnet.egress.internet`

Whether outbound `fetch()` from the workload's Worker, Durable Objects and
Node app may reach the public internet, and which hosts a container
started with `enableInternet: true` may reach
([containers.md](containers.md)). Tailnet egress is not affected.

| value | Workers and Node apps | containers (`enableInternet: true`) |
|---|---|---|
| absent | any public host | none |
| `true` | any public host | any public host |
| `false` or `[]` | none | none |
| `["api.example.com", "*.github.com", "203.0.113.0/24"]` | only these | only these |

```jsonc
{ "tailnet": { "tags": ["tag:tw-wl-app"], "egress": { "internet": false } } }
```

- Internet traffic leaves from the **host's own network**, not from a
  tailnet node, so the other side sees the host's address (each host has
  its own) and no tailnet identity. For a fixed source address, use a
  gateway on your tailnet ([recipes.md](recipes.md#call-an-internet-api)).
- Loopback, private, link-local (cloud metadata), CGNAT and tailnet
  ranges and the host's own addresses are always refused
  (`forbidden_destination`), even when listed, and even through a DNS name
  that resolves to one. Names under `.internal`, `.local`, `.localhost`,
  `.lan` and other special-use suffixes are never looked up.
- A destination the setting refuses gets `internet_egress_unavailable`.
  The host's operator can also turn internet egress off for every
  workload on a host (`tw up --worker-internet=off`, or
  `tw-hostd -worker-internet=off`; `tw status` and `tw doctor` show it).
- A list entry is an exact host name, `*.suffix` (any name under it),
  `*` (any name), an IP or a CIDR. An IP literal in a URL matches only IP
  and CIDR entries.
- An entry may name a TCP port: `"smtp.example.com:25"`, `"*:25"`,
  `"203.0.113.0/24:25"`, `"[2001:db8::1]:25"`. An entry with a port allows
  that port only (add a second entry without one for the host's other
  ports); an entry without a port allows every port except 25.
- **TCP port 25 is refused by default** (`internet_port_refused`), with or
  without a list, because internet flows leave from the host's own address
  and mail relayed from a workload would come from you. Only an entry that
  names the port allows it (`"smtp.example.com:25"`). Submission ports 465
  and 587 are not affected ([security-model.md](security-model.md#egress)).
- A change takes effect when the deployment that makes it becomes active.
- `true` and `false` need hosts that offer the `worker-internet` feature,
  and an entry that names a port needs `internet-ports`
  ([upgrading.md](upgrading.md)); `[]` and plain entries work on older
  hosts too.
- A host can send all of its internet egress through a tailnet exit node
  instead of its own network ([`egress.via`](#tailnetegressvia)).

### `tailnet.egress.via`

Where the workload's internet egress may leave from. `"exit-node"` requires
a host whose internet path is a tailnet exit node, so the destination sees
the exit node's address, not the host's, and the host's resolver never
sees the names. Absent (the default): whatever the host offers.

```jsonc
{
  "tailnet": {
    "tags": ["tag:tw-wl-billing"],
    "egress": { "internet": ["api.stripe.com"], "via": "exit-node" }
  }
}
```

- `via` says *how*; `internet` says *whether and where to*. Tailnet egress
  is not affected: it always leaves from the workload's or object's own
  node.
- It is enforced at placement. A host advertises the `internet-exit-node`
  feature only when its operator started it with
  `tw up --worker-internet=exit-node=<node>` (or `tw-hostd
  -worker-internet=exit-node=<node>`, with `-container-internet=exit-node`).
  A deploy with `via` is placed only on such hosts, and otherwise fails
  with `feature_unavailable`. A host on this path sends **every**
  workload's internet traffic through its exit node, `via` or not; `via`
  is how a workload refuses to run anywhere else.
- `<node>` is the exit node's MagicDNS name, host name, tailnet IP or node
  ID. The host's own node (`tag:tw-host`) uses it, so the tailnet policy
  must grant `tag:tw-host` → `autogroup:internet`, and the exit node must
  advertise exit routes that the tailnet admin approved. tailworks never
  writes either.
- Names resolve through the exit node, never through the host's resolver:
  by default at the exit node itself (its peer API, as Tailscale clients
  do). Some exit nodes don't answer DNS for tagged peers; then add a
  resolver, `exit-node=<node>,dns=1.1.1.1`, and names resolve there,
  through the exit node. The SSRF guard still checks every answer and
  dials the checked address.
- When the exit node is missing, not offered, offline or refuses DNS, the
  host refuses internet egress with `exit_node_unavailable`, never falls
  back to its own network; `tw status` and `tw doctor` say why.
- `tw dev` has no exit node: it runs the workload on your machine's
  network and prints a note.

### `tailnet.https`

`true` also serves the workload on
`https://<name>.<tailnet>.ts.net`, port 443, with the workload node's own
ts.net certificate, for code that needs an HTTPS origin: an OAuth
server's resource URL (`@cloudflare/workers-oauth-provider`), `Secure`
cookies, redirect URIs. A request that arrives that way reaches the Worker
with an `https://` `request.url` (as on Cloudflare) and
`X-Forwarded-Proto: https`; a Node app gets the header
([issue #360](https://github.com/rosskukulinski/tailworks/issues/360)).

```jsonc
{ "tailnet": { "tags": ["tag:tw-wl-mcp"], "https": true } }
```

- **`http://` keeps working** on port 80, unchanged and not redirected:
  `ctx.tailnet.fetch`, service bindings and other workloads call it, and a
  redirect would turn their `POST`s into `GET`s. A request on port 80 still
  has an `http://` `request.url`, so give clients the `https://` URL
  (`tw deploy` prints it).
- Callers need a grant to the workload's tags on **`tcp:443`** as well as
  `tcp:80`. With `tailnet.https` on, the caller grant `tw deploy` and
  `tw policy` print, and the one `tw policy --apply` (or
  `--apply-policy`) writes for a new app, covers both ports. A grant you
  wrote yourself, or one tailworks wrote before you turned `https` on,
  needs `"tcp:443"` added to its `ip`.
- The tailnet must issue HTTPS certificates (admin console, DNS page:
  MagicDNS and HTTPS Certificates). Without them the deploy fails with
  [`tailnet_https_unavailable`](errors.md#tailnet_https_unavailable) and
  the deployment that was active keeps serving. Headscale issues no ts.net
  certificates, so it can't serve `tailnet.https`
  ([headscale.md](headscale.md)).
- A new name's first certificate takes 30 to 100 seconds to issue. The host
  requests it as soon as the listener opens, but the first `https://` call
  right after the first deploy can wait for it.
- Only the workload node: an object's own node (`tailnet.objects`) and a
  Tailscale Service name (`tailnet.service`) stay `http://`. Public routes
  are HTTPS already (Funnel).
- `tw dev` serves the app on its local `http://` URL only and prints a note.
- It needs hosts that offer the `tailnet-https` feature
  ([upgrading.md](upgrading.md)).

### `tailnet.objects.<Class>`

`<Class>` is a class name from `wrangler.jsonc` `durable_objects.bindings`.
A class listed here with no matching binding is an error. A class that
isn't listed is internal, as with `"node": false`: a plain Durable Object
with no node of its own.

| key | type | default | what it does |
|---|---|---|---|
| `tags` | `["tag:…"]` | | tags of each instance's own node |
| `ttl` | string, e.g. `"10m"` | | lifetime from creation: then the object (and its sub-agents) is killed. The default for `spawn()`, which can set its own |
| `node` | boolean | `true` | `false`: an internal class with no tailnet node. Its outbound `fetch()` leaves as the workload node, so it needs no `egress` |
| `egress` | `"object"` \| `"workload"` | `"object"` (`"workload"` with `"node": false`) | whose node outbound `fetch()` leaves from, for a class with its own node ([egress-modes.md](egress-modes.md)). Changing it later empties the class's storage |
| `fence` | `"never"` | | instances tolerate running on two hosts at once, so mgmt can move the workload off a dead host without `--force-takeover` |
| `public.create` | boolean | `false` | public requests may create new objects of this class ([public-ingress.md](public-ingress.md)) |
| `public.maxObjects` | integer | | required with `create`: the most objects public requests may create |

An older `"agent": true` here is accepted and ignored: an Agents SDK class
needs no flag.

## `placement`

How mgmt picks a host when there is more than one
([hosts.md](hosts.md#where-a-workload-runs-placement-multi-host)).

| key | type | default | what it does |
|---|---|---|---|
| `mode` | `"smart"` \| `"off"` | `"smart"` when there are targets | `"off"` places by free capacity only |
| `targets` | `[tag or name]` | `tailnet.connects` | the peers to place close to |

Advanced: `aggregate` (`"worst"`, the default, or `"sum"`) is how several
targets combine. Cloudflare's placement hints `host`, `hostname` and
`region` still work here but the schema marks them deprecated: `host` and
`hostname` are a one-element `targets`, and `region` is ignored. Without a
`placement` here, the wrangler config's `placement` is read instead, hints
and all.

## `public`

Paths served to the public internet over Funnel
([public-ingress.md](public-ingress.md)). Without `public` the workload is
tailnet-only.

| key | type | what it does |
|---|---|---|
| `routes` | `["/x", "/x/*", "/*"]` | **Required.** exact paths, prefixes, or everything |
| `upgrade` | `[route]` | routes (also in `routes`) that may upgrade to WebSocket or SSE |
| `limits` | object | `rps`, `burst`, `ipRps`, `ipBurst`, `maxBodyBytes`, `maxHeaderBytes`, `timeoutSeconds`, `maxConns`, `maxConnsPerIp`, `upgradeLifetimeSeconds`, up to tailworks' ceilings |

## Tags and `tw init`

`tw init <dir> --template …` names the template's tags after your app:
`tag:tw-wl-<workload>` for the workload node, and
`tag:tw-<workload>-<class>` for each class (`SupportAgent` becomes
`support-agent`). Two apps made from the same template then don't share
tags, so they don't share grants either. Templates whose workload needs
nothing else (`hello`, `static-site`, `python`) leave `tailnet.tags` out:
it defaults to `tag:tw-wl-<workload>`. To use tags you already own
instead, `--tags` sets the workload's tags (adding `tailnet.tags` when the
template has none) and `--object-tags` sets the classes' ([below](#reusing-existing-tags)).

Each new tag needs a `tagOwners` entry in your tailnet policy. `tw deploy`
checks this first and prints the lines to add:

```jsonc
"tagOwners": {
  "tag:tw-wl-support":            ["autogroup:admin", "tag:tw"],
  "tag:tw-support-support-agent": ["autogroup:admin", "tag:tw"],
  ...
}
```

`tw policy --apply` (or `tw deploy --apply-policy`) writes those lines for
you, along with the `tailnet.connects` grants, for `tailnet.service` the
`autoApprovers.services` entry that lets the workload's own tags host its
Service (only that `svc:` name, only those tags), and, for `public` routes,
`tag:tw-public` and its funnel `nodeAttrs`. For a new app it also writes
the caller grant ([below](#the-caller-grant)). It prints the lines first and
asks, or takes `--yes` when there's no terminal. mgmt then reads the policy
with its ETag, merges in only the missing lines (comments kept, nothing
changed or removed, nothing outside `tag:tw*` but the caller grant's
source), validates the result and
writes it with `If-Match`. If someone saved the policy in the meantime, it
retries once, then stops with
[`policy_conflict`](errors.md#policy_conflict). The write is audited
(`tw audit --all`, kind `policy_apply`). It needs the tenant's OAuth client
to have the **Policy File** write scope (not just Read). Without that scope,
copy the snippet `tw policy` prints into the admin console.

When `tailnet.connects` names another tailworks app's tag that isn't defined
yet (say `tag:tw-wl-mcp`, before `mcp` is deployed), `tw policy` adds its
`tagOwners` entry, the same one deploying `mcp` would add, so the grant to
it can be written. A tenant with its own owner tag (a team) doesn't get
this: the tag may belong to another team's app, so you define it yourself.

Some lines are never written: a grant to `*` or `autogroup:*`, a grant to a
tag outside `tag:tw*` that your policy doesn't define (such as `tag:db`
before you add it to `tagOwners`, which the control plane would reject), and a
grant for callers of a Service's `svc:` name. `tw policy`
lists them for you to add yourself.

#### The caller grant

Nothing on the tailnet reaches a new app until a grant allows it. So that
the first `curl` works, `tw policy --apply` and `tw deploy --apply-policy`
add one with a new app's tag:

```jsonc
{"src": ["autogroup:member"], "dst": ["tag:tw-wl-support"], "ip": ["tcp:80"]}
```

It lets **every user of the tailnet** (not tagged devices) call the app on
port 80. It is in the lines tailworks shows before it asks, with a
sentence saying so. tailworks writes it only when all of these hold:

- the same write adds the app's workload tags to `tagOwners`, so the app
  is new to your policy. For a tag the policy already defines it writes
  nothing: not for a second app that reuses the tag, and not again after
  you removed or narrowed the grant;
- the tags are tailworks app tags (`tag:tw-…`, never `tag:tw`,
  `tag:tw-mgmt`, `tag:tw-host` or `tag:tw-public`);
- no grant or ACL of yours from a user, group, autogroup, `*` or a tag
  outside `tag:tw*` already reaches the tag or every node (`"dst": ["*"]`,
  as in Tailscale's default policy);
- the control plane is Tailscale (Headscale's policy has no
  `autogroup:member` source here).

The source and port are fixed in tailworks. Nothing in `tailworks.jsonc`
can change them, and a `tailnet.connects` entry can't produce a grant from
anyone but the app's own tags.

To choose the callers yourself, say no at the question and add your own
grant to the app's tag (a user, a group, another tag) before you deploy, or
deploy without `--apply-policy`. When tailworks doesn't write the grant and
nobody can call the app, `tw policy --apply` says so and prints the line.
For a Service, callers of the `svc:` name need their own grant; tailworks
doesn't write that one.

If you'd rather not edit the policy for every app, reuse a small set of tags
that an admin owns once, e.g. `tag:tw-app` for workloads and
`tag:tw-agent` for agent classes. The catch: every app with the same tag
gets the same grants. Grants are made per tag, so per-app tags are the only
way to give two apps different access.

### Reusing existing tags

When your tailnet policy is managed by someone else, `tw init` can use tags
the policy already has, so a new app needs no policy change:

```sh
tw init billing --template agent --tags tag:tw-app --object-tags tag:tw-agent
tw policy    # nothing to add when the policy already owns and grants these tags
```

- `--tags tag:a,tag:b` sets the workload node's `tailnet.tags`.
- `--object-tags tag:a,tag:b` sets the tags of every Durable Object class
  that has its own node. `--object-tags <Class>=tag:a,tag:b` sets one class,
  and wins over the form without a class. Repeat the flag for more classes.
- `--tags` adds `tailnet.tags` when the template leaves it out (`hello`,
  `static-site`, `python`).
- Tags you don't set are still named after the app.

`tw init` checks the tags before it writes anything: they must be valid
tags, can't be one of tailworks' own tags
([`reserved_tag`](errors.md#reserved_tag)), and each class you name must
have a node of its own in the template. The policy itself is checked later,
by `tw policy` and `tw deploy`'s preflight: the tags still need a
`tagOwners` entry that includes `tag:tw` (or your team's owner tag,
[teams.md](teams.md)), and the grants for `tailnet.connects`.

The tags are written to `tailworks.jsonc`, which has no environments, so
`tw deploy --env <name>` uses the same tags. A project with only a wrangler
config instead gets `tag:tw-wl-<name>-<env>` (unless the env sets its own
`name`), a new tag per environment: add a `tailworks.jsonc` to keep one set.
