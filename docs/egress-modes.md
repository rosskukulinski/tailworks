# Per-object egress: facet mode vs plain-DO mode

Each Durable Object class picks how its outbound `fetch()` is attributed, in `tailworks.jsonc`:

```jsonc
{ "tailnet": { "objects": {
    "SupportAgent": { "tags": ["tag:tw-agent-support"] },                     // facet mode (default)
    "Reminder":     { "tags": ["tag:tw-reminder"], "egress": "workload" },    // plain-DO mode
    "TwBudget":     { "node": false }                                          // internal: no node at all (egress as the workload)
} } }
// A class left out of "objects" is internal, as if it said { "node": false }.
```

| | **facet** (default, `"egress": "object"`) | **plain-DO** (`"egress": "workload"`) |
|---|---|---|
| How it runs | The platform's `Host_<Class>` DO hosts your class as a facet in a Dynamic Worker | Your class is an ordinary DO in the tenant service |
| Egress identity | **The object's own node.** `globalOutbound` is the platform `Egress` specialised with this object's class+name (props tenant code can't change), and hostd verifies a MAC | **The workload node**, shared by every object of the workload |
| Ingress identity | Own node (`getCaller()` per request) | Own node (`getCaller()` per request) |
| Raw TCP (`connect()`, ADR-052) | Stock `connect()` and `ctx.tailnet.connect()` leave as **the object's own node** (the tunnel's preamble carries the same MAC'd identity) | Stock `connect()` leaves as **the workload node**; `ctx.tailnet.connect()` throws `not_supported` (no per-object identity before WD-3) |
| Alarms (`setAlarm`, Agents SDK `schedule()`) | ✅ Delegated to the `Host_<Class>` DO, which runs the facet's `alarm()` | ✅ |
| `ctx.tailnet.spawn/kill` | Attested by the platform (unforgeable `as`) | Claimed by the tenant-side wrapper. Within one workload, code could claim to be another plain object of the same workload. Same trust domain (RFC §4.8 residual risk) |
| Creating a new object through `env.X` | Admitted by the request's ticket or the object's attested identity ([create admission](create-admission.md)); a raw binding is refused `create_not_admitted` | Same; a plain DO whose creation was not admitted answers `create_not_admitted` and gets no node |
| Kill | Enforced in platform code (the `Host_` flag, plus the facet aborted) | Enforced by the tenant-side wrapper's `fetch`; the node is killed by netd either way |
| `ctx.container` (classes with wrangler `containers`, docs/containers.md) | ✅ Only the object's own container (platform-attested) | ✅ Its own container; like `spawn/kill`, claimed by the tenant-side wrapper, so code in the workload could drive another plain object's container of the same workload, never a facet object's |
| A container's intercepted requests (`interceptOutboundHttp(s)`, docs/containers.md) | Run in the object; the handler's `fetch()` leaves as **the object's node** | Run in the object; the handler's `fetch()` leaves as **the workload node** |
| Env bindings to other DOs | Proxies: `env.X.getByName(name)`, or `env.X.get(env.X.idFromName(name))`. Such an id's `toString()` is the object's name; `idFromString` and `newUniqueId` fail with `bad_request` | Real namespaces |
| `newUniqueId()` objects | Not supported (objects must have names; a name is the node's identity) | Work, but get no node (no `ctx.id.name`) |

Switching a class between modes changes its storage namespace (`Host_<Class>` vs `<Class>`), so existing objects of that class start empty. Pick the mode before you store data. The fix for both columns is upstream ask WD-3 (per-actor `globalOutbound`), which removes the facet requirement and gives alarms *and* per-object egress.

**A class missing from `tailnet.objects`** (a `tailworks.jsonc` that lists some classes, or only `tailnet.tags`, or no `tailworks.jsonc` at all) is internal, exactly as with `"node": false`: a plain DO with no node of its own, as on Cloudflare (`idFromName`, `newUniqueId`, storage under `<Class>`). `tw` writes the `"node": false` into the `tailworks.jsonc` it packs. Its `fetch()` and `connect()` leave as the workload node, `ctx.tailnet.connect()` needs the class listed, and no create admission applies to it. List the class with `tags` to give each of its objects a node, in facet mode; that changes its storage namespace as above.

Before #492 a class left out of a `tailworks.jsonc` ran in facet mode with no node, its storage under `Host_<Class>`. A bundle packed by an older `tw` keeps that until it is redeployed. To keep that storage across the redeploy, list the class with `tags` (facet mode).

**Hibernation (both modes).** workerd evicts idle objects after about 10 s, which drops in-memory state. The object's node stays online in netd, and the next request wakes the object (measured in `TestHostd/HibernationKeepsNode`).
