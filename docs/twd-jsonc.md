# twd.jsonc reference

A twd app has a `twd.jsonc` next to its wrangler config (`wrangler.jsonc`,
`wrangler.json` or `wrangler.toml`; [wrangler.md](wrangler.md)). The split:

- **The wrangler config** is the Worker itself, as on Cloudflare: `main`,
  `compatibility_date`, `durable_objects` bindings, `migrations`, `vars`,
  `triggers.crons`, `queues` ([queues.md](queues.md)), `containers`,
  `d1_databases` ([d1.md](d1.md)), and `env.<name>` environments
  (`twd deploy --env`).
- **`twd.jsonc`** is what twd adds: the tailnet identity of the workload and
  of each Durable Object class, the isolation tier, placement, and public
  routes.

`twd.jsonc` is JSONC: comments and trailing commas are fine.

A project with only a wrangler config deploys too. Then twd uses defaults:
the workload node is tagged `tag:twd-wl-<workload>`, and every Durable
Object class is internal (`"node": false`). `twd deploy` says when it does
this ([wrangler.md](wrangler.md#without-a-twdjsonc)).

## Validation and editor completion

`twd dev` and `twd deploy` check `twd.jsonc` against
[`schema/twd.schema.json`](../schema/twd.schema.json) before doing anything
else. An unknown key or a bad value stops the command:

```
error [bad_request]: twd.jsonc has 1 problem
  tailnet.objcts: unknown key (did you mean "objects"?)
```

This matters most for `tailnet.objects`. If that key is misspelt, every
class deploys as an ordinary Durable Object with no node of its own, and
nothing else would tell you.

For completion and inline errors in your editor, point `$schema` at the
schema. `twd init` writes this line for you:

```jsonc
{
  "$schema": "https://raw.githubusercontent.com/rosskukulinski/twd/master/schema/twd.schema.json",
  "name": "support",
  ...
}
```

## Top-level keys

| key | type | default | what it does |
|---|---|---|---|
| `$schema` | string | | the schema, for your editor; twd ignores it |
| `name` | string | wrangler `name`, then the directory name | the workload name, a DNS label. `twd deploy --name` overrides it |
| `runtime` | `"workerd"` \| `"node"` | `"workerd"` | `"node"` runs a Node.js app in the Node process tier ([node-tier.md](node-tier.md)); then there is no `wrangler.jsonc` |
| `main` | string | | `runtime: "node"` only: the entry file, e.g. `"src/server.ts"` |
| `node` | object | | `runtime: "node"` only: `env` (string map), `listen`, `health`, `args` ([node-tier.md](node-tier.md)) |
| `tier` | `"process"` \| `"microvm"` | the tenant's tier | `"microvm"` runs the workload in a Firecracker VM, on a Linux host with KVM ([INSTALL.md](INSTALL.md#linux-the-microvm-tier-firecracker-kvm)) |
| `replicas` | integer ≥ 1 | 1 | run a stateless workload on this many hosts ([services.md](services.md)) |
| `placement` | object | | which host runs the workload; [below](#placement) |
| `public` | object | | paths reachable from the public internet over Funnel; [below](#public) |
| `containers` | array or object | | container classes ([containers.md](containers.md)); `twd deploy` also writes image pins here |
| `tailnet` | object | | the workload's tailnet identity; [below](#tailnet) |

## `tailnet`

| key | type | what it does |
|---|---|---|
| `tags` | `["tag:…"]` | **Required.** Tags of the workload's own node. Your policy must let `tag:twd` own them; `twd deploy` prints the snippet if it doesn't |
| `connects` | `["tag:…" or name]` | what the workload calls. `twd deploy` preflights these grants, and smart placement measures paths to them |
| `service` | `true` or `"svc:<name>"` | a Tailscale Service: a stable name and VIP in front of the workload ([services.md](services.md)) |
| `egress.internet` | `[host, "*.suffix", IP, CIDR]` | internet destinations the workload's **containers** may reach when started with `enableInternet: true` ([containers.md](containers.md)). Worker code stays tailnet-only either way; for internet APIs, see [recipes.md](recipes.md#call-an-internet-api) |
| `spawn.maxLive` | integer | the most live sub-agents (`ctx.tailnet.spawn`) |
| `spawn.maxDepth` | integer | the deepest spawn chain |
| `objects` | map of class → [object](#tailnetobjectsclass) | the Durable Object classes that get a tailnet node per instance |

### `tailnet.objects.<Class>`

`<Class>` is a class name from `wrangler.jsonc` `durable_objects.bindings`.
A class listed here with no matching binding is an error.

| key | type | default | what it does |
|---|---|---|---|
| `tags` | `["tag:…"]` | | tags of each instance's own node |
| `agent` | boolean | `false` | the class is an Agents SDK `Agent` |
| `ttl` | string, e.g. `"10m"` | | lifetime from creation: then the object (and its sub-agents) is killed. The default for `spawn()`, which can set its own |
| `node` | boolean | `true` | `false`: an internal class with no tailnet node (implies `"egress": "workload"`) |
| `egress` | `"object"` \| `"workload"` | `"object"` | whose node outbound `fetch()` leaves from ([egress-modes.md](egress-modes.md)). Changing it later empties the class's storage |
| `fence` | `"never"` | | instances tolerate running on two hosts at once, so mgmt can move the workload off a dead host without `--force-takeover` |
| `public.create` | boolean | `false` | public requests may create new objects of this class ([public-ingress.md](public-ingress.md)) |
| `public.maxObjects` | integer | | required with `create`: the most objects public requests may create |

## `placement`

How mgmt picks a host when there is more than one
([INSTALL.md](INSTALL.md#where-a-workload-runs-placement-multi-host)).

| key | type | default | what it does |
|---|---|---|---|
| `mode` | `"smart"` \| `"off"` | `"smart"` when there are targets | `"off"` places by free capacity only |
| `targets` | `[tag or name]` | `tailnet.connects` | the peers to place close to |
| `aggregate` | `"worst"` \| `"sum"` | `"worst"` | how several targets combine |
| `host`, `hostname` | string | | Cloudflare's placement hints: one target |
| `region` | string | | accepted for Cloudflare compatibility and ignored |

## `public`

Paths served to the public internet over Funnel
([public-ingress.md](public-ingress.md)). Without `public` the workload is
tailnet-only.

| key | type | what it does |
|---|---|---|
| `routes` | `["/x", "/x/*", "/*"]` | **Required.** exact paths, prefixes, or everything |
| `upgrade` | `[route]` | routes (also in `routes`) that may upgrade to WebSocket or SSE |
| `limits` | object | `rps`, `burst`, `ipRps`, `ipBurst`, `maxBodyBytes`, `maxHeaderBytes`, `timeoutSeconds`, `maxConns`, up to twd's ceilings |

## Tags and `twd init`

`twd init <dir> --template …` names the template's tags after your app:
`tag:twd-wl-<workload>` for the workload node, and
`tag:twd-<workload>-<class>` for each class (`SupportAgent` becomes
`support-agent`). Two apps made from the same template then don't share
tags, so they don't share grants either. `--keep-tags` keeps the template's
tag names.

Each new tag needs a `tagOwners` entry in your tailnet policy. `twd deploy`
checks this first and prints the lines to add:

```jsonc
"tagOwners": {
  "tag:twd-wl-support":            ["autogroup:admin", "tag:twd"],
  "tag:twd-support-support-agent": ["autogroup:admin", "tag:twd"],
  ...
}
```

If you'd rather not edit the policy for every app, reuse a small set of tags
that an admin owns once, e.g. `tag:twd-app` for workloads and
`tag:twd-agent` for agent classes. The catch: every app with the same tag
gets the same grants. Grants are made per tag, so per-app tags are the only
way to give two apps different access.
