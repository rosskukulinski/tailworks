# Limits

Every size, count, rate and timeout tailworks enforces, in one place, with the
code that sets it. Limits apply per host and per workload unless a
row says otherwise (a tenant quota covers all of a tenant's workloads).
**Configurable** names the flag or config key that changes a limit; "no"
means it is a constant. Several limits copy Cloudflare's so that code
written for Workers behaves the same; the feature pages explain the
differences. [Limits that don't exist](#limits-that-dont-exist) lists what
tailworks deliberately does not bound.

**Source** cites code as `` `path:target` ``, never by line number, so the
citation survives edits around it. `target` is one of:

- `Name`: a Go `const` or `var` (at any scope), or else the one assignment
  `x.Name = value`, field `Name: value` or `def(&x.Name, value)` in the
  file; in JS or TS, `const NAME = value` or `NAME: value`.
- `Name.Key`: a field or map key inside `Name`'s literal
  (`Defaults.RPS`, `D1_LIMITS.params`).
- `-flag`: a flag definition, `x.Int("flag", default, …)`.
- `#slug`: a `// limits.md#slug` comment in the code, on the cited line or
  alone on the line above it. Use one when the value isn't a named
  constant.

`TestLimitsDocCitations` (`docs/limits_test.go`, part of `make test`) fails
when a citation stops resolving to exactly one place, or when the cited code
is a constant expression whose value isn't one of the quantities in the
row (any common unit counts: `64 MiB` matches `64 << 20`, `2 min` matches
`2*time.Minute` or `120`). When you change a limit, change its row here too.

## Limits that don't exist

These were checked in the workerd config tailworks generates (`hostd/gen`), in
workerd's config schema (`workerd.capnp`), and in the proxies a tailnet
request passes through (netd, then hostd, then workerd). Each is
deliberately not provided, for the reason given. What this means for
tenants sharing a host is in
[security-model.md](security-model.md#availability-between-tenants).

| Limit | Value | Deliberately not provided, because |
|---|---|---|
| CPU time per request, alarm, cron or queue run | none | workerd's config schema has no CPU limit to set (Cloudflare's CPU enforcement is not part of open-source workerd), and `RLIMIT_CPU` counts a process's CPU over its whole life, so it would kill a long-running workload rather than a runaway request. wrangler's `limits` key is ignored (`cmd/tw/wranglercfg.go:#wrangler-limits-ignored`). Cron and queue runs have wall-time limits (below) |
| Memory of a process-tier process beyond the V8 heap | none | the V8 heap is capped (see [Process tier](#process-tier)); a cap on the whole process would need `RLIMIT_AS`, which breaks V8 (it reserves far more address space than it uses), or a cgroup, which needs a delegated cgroup subtree from the service manager. macOS enforces neither. On the [microVM tier](#hosts-and-the-microvm-tier) the guest's memory is the bound |
| Wall time of a tailnet request | none | streamed responses and WebSockets are long-lived by design, and a request is cancelled when its caller goes away (`netd/ingress.go:#tailnet-no-deadline`). Requests from the internet do have one: see [Public ingress](#public-ingress) |
| Tailnet request body size | none | bodies are streamed through netd and hostd to workerd, never buffered |
| Request rate over the tailnet | none | every caller is a WireGuard-identified node your tailnet policy admitted; public traffic is rate-limited (see [Public ingress](#public-ingress)) |
| Outbound `fetch()` duration or count (subrequests) | none | every `fetch()` goes through netd: tailnet egress leaves as the workload's or object's own node, where your tailnet policy decides what it can reach, and internet egress is allowed, narrowed or turned off per workload (`tailnet.egress.internet`) |
| New internet flows per workload per unit of time (`fetch()`, `connect()`, `node:net`) | none | a per-workload token bucket on new internet flows needs a design for the typed refusal and for the rolled-up audit record of a refused flood, as public ingress has; until then a workload that is allowed internet egress can open flows from the host's address as fast as the host lets it, and every TCP flow and every first `fetch()` per destination is audited (ADR-052 follow-up, [#339](https://github.com/rosskukulinski/tailworks/issues/339)). What is bounded: TCP port 25 is refused by default (`internet_port_refused`), an object has at most 256 open sockets, and `tailnet.egress.internet: false` or a list is the control ([security-model.md](security-model.md#egress)) |
| Concurrent requests per workload or object | none | WebSockets make an in-flight count a poor bound (one Durable Object can hold thousands), and a flood of requests to one workload mostly costs that workload's own workerd |
| WebSocket duration | none | as for wall time |
| Queue message retention | none | a message is kept until it is consumed; a full queue refuses new sends ([queues.md](queues.md#differences-from-cloudflare)) |

## Deploy and bundles

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Deploy request body (the bundle, JSON-encoded) | 64 MiB | no | `mgmt/server.go:#mgmt-body` |
| Unpacked bundle, all files | 64 MiB | no | `hostd/bundle/bundle.go:MaxBundle` |
| Static assets (`assets.directory`), counted in the bundle | 64 MiB (about 48 MiB fits one deploy request) | no | `hostd/bundle/bundle.go:MaxBundle` |
| `_redirects` rules: static, dynamic | 2000, 100 | no | `hostd/bundle/assetrules.go:MaxStaticRedirects`, `hostd/bundle/assetrules.go:MaxDynamicRedirects` |
| `_redirects` line | 1000 characters | no | `hostd/bundle/assetrules.go:MaxRedirectLine` |
| `_headers` rules | 100 | no | `hostd/bundle/assetrules.go:MaxHeaderRules` |
| `_headers` line | 2000 characters | no | `hostd/bundle/assetrules.go:MaxHeaderLine` |
| Bundle sent into a microVM guest | 256 MiB | no | `guest/tw-guestd/main_linux.go:maxBundle` |
| `tw deploy` wait for ready | 3 min | `tw deploy --wait` | `cmd/tw/placementflags.go:-wait` |
| One host bringing up a deployment | 2 min | no | `hostd/workload.go:#deploy-bringup` |
| In-flight requests on the old instance at a redeploy (then it stops) | 10 s | no | `hostd/host.go:DrainTimeout` |
| Replicas of a stateless workload | 16 | `tailworks.jsonc` `replicas` | `mgmt/workload_replicas.go:MaxReplicas` |

## Requests

See [Limits that don't exist](#limits-that-dont-exist) for duration, body and CPU.

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Request headers on an object's tailnet ingress | 1 MiB (Go's default) | no | `netd/registry.go:#object-ingress-server` |
| Time to send request headers | 30 s | no | `netd/registry.go:#object-ingress-header-timeout` |
| Caller lookup (WhoIs) per request | 5 s, then `no_identity` | no | `netd/ingress.go:#whois-timeout` |
| Wait for a running instance during a redeploy | 15 s (the 10 s drain plus 5 s), then `upstream_unavailable` | no | `hostd/http.go:#redeploy-wait` |
| Wait for an object's node (`identity_pending` after) | 30 s | no | `hostd/host.go:ReadyTimeout` |
| `Retry-After` on `identity_pending` | 1 s | no | `mgmt/server.go:#identity-pending-retry-after` |
| Node-runtime caller header clock skew | 60 s | `maxSkewMs` in `@tailworks/node` | `js/packages/node/src/index.ts:DEFAULT_MAX_SKEW_MS` |
| Open `connect()` sockets per object (the workload node counts as one, for the Worker and plain-DO objects), per instance | 256, then `socket_limit` | no | `hostd/connect.go:maxSocketsPerObject` |
| Time for the platform to authenticate a `connect()` tunnel | 10 s | no | `hostd/connect.go:connectPreambleTimeout` |
| `connect()` dial (and `tls: "netd"` handshake) | 30 s, as an object's node wait | no | `hostd/host.go:ReadyTimeout` |
| A service binding RPC call's encoded arguments, or its result | 32 MiB (`RangeError` / `bad_request` over it) | no | `hostd/gen/js/svc.js:RPC_MAX_BYTES` |
| How long hostd reuses a service binding's resolved target | 5 s (dropped sooner when netd can't reach it) | no | `hostd/servicebind.go:svcTargetTTL` |
| Wait for mgmt to accept a new object's ready report (and so issue its node certificate) before its first outbound call | 20 s, then `identity_pending` (a failed report is re-sent at most every 5 s) | no | `hostd/egressgate.go:#egress-report-wait`, `hostd/egressgate.go:rereportEvery` |
| How long a receiver reuses a caller's node certificate; a "not a tailworks object" answer | 60 s; 2 s | no | `hostd/certs.go:certCacheTTL`, `hostd/certs.go:#cert-no-cert-ttl` |

## Process tier

Per workload, on every process-tier host (macOS and Linux). Flags are
`tw-hostd`'s; `tw up` uses the defaults. Past the heap cap V8 aborts the
workload's whole workerd (every isolate in it, so all of the workload's
Durable Objects too) or Node process, and hostd restarts it like any
other crash.

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| V8 heap of each isolate in a workerd (the Worker, each Durable Object class) | 1024 MiB | `-workerd-heap-mib` (-1: no cap) | `hostd/limits.go:DefaultWorkerdHeapMiB` |
| V8 heap of a Node-runtime app (`--max-old-space-size`) | 2048 MiB | `-node-heap-mib` (-1: no cap); the app's own `--max-old-space-size` in `node.args` or `NODE_OPTIONS` wins | `hostd/limits.go:DefaultNodeHeapMiB` |
| Process memory beyond the heap, CPU | none | — | [Limits that don't exist](#limits-that-dont-exist) |

## Durable Objects

See also [create-admission.md](create-admission.md).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Live objects per tenant | 1000 | `tw tenant set --max-objects` | `mgmt/types.go:DefaultMaxObjects` |
| Spawn depth below a root object | 3 (a deployment may only lower it) | `tailworks.jsonc` `tailnet.spawn.maxDepth` | `mgmt/types.go:DefaultMaxDepth`, `mgmt/service.go:#spawn-lower` |
| Live descendants of one root object | 64 (a deployment may only lower it) | `tailworks.jsonc` `tailnet.spawn.maxLive` | `mgmt/types.go:DefaultMaxLive`, `mgmt/service.go:#spawn-lower` |
| Node registrations per tenant | 200 per minute (tailworks' own throttle) | `tw tenant set --registrations-per-minute` | `mgmt/types.go:DefaultRegistrationsPerMinute` |
| Create ticket lifetime | 2 min | no | `hostd/gen/mac.go:TicketTTL` |
| `ready()` wait after a spawn | `timeoutMs`, at most 5 min (else 30 s) | per call | `hostd/http.go:#ready-wait-max` |
| Node join after a create | 3 min | no | `hostd/objects.go:#node-join-after-create` |
| Retries of a failed node join | 5 (1, 2, 4, 8, 16 s) | no | `hostd/objects.go:maxTries` |
| Storage per object (workerd tier) | none | — | |
| Storage per object (microVM tier) | 1024 MiB volume (sparse) | `tw-hostd -vm-volume-mib` | `cmd/tw-hostd/main.go:-vm-volume-mib` |

## Tailnet node budget

Every node tailworks runs for an app is a device on your tailnet. **Your
Tailscale plan's device limit applies to those nodes** as it does to any
other device: see [Tailscale's pricing page](https://tailscale.com/pricing)
for what each plan includes. tailworks doesn't know your plan and can't
check against it, so count before you deploy a class with many objects.

What an app uses:

| In `tailworks.jsonc` | Nodes |
|---|---|
| The workload (`tailnet.tags`) | 1, the workload node. It is one node wherever the workload runs, not one per host |
| `"replicas": N` | N: each replica is a copy of the workload on its own host, with its own workload node |
| `"public"` routes | 1 more per copy: the public node |
| A class in `tailnet.objects` with `tags` | 1 per object, in facet mode and in plain-DO mode alike. `"egress"` picks which node outbound requests leave from, not whether the object has a node. A sub-agent (`ctx.tailnet.spawn()`) is an object |
| A class with `"node": false` | 0: its objects are reached through the workload |
| Containers | 0 |

Also on the tailnet, and not in `tailworks.jsonc`:

- **Previews.** `tw deploy --preview` runs a second copy of the workload,
  with its own workload node and its own objects' nodes, until `tw promote`
  or `tw undeploy`.
- **Pool nodes.** tailworks keeps one pre-joined node per class that had a
  new object on that host in the last hour ([concepts.md](concepts.md#pool-node-_pool)).
  `tw up --pool-nodes 0` turns them off.
- **tailworks itself.** The default `tw up` is one node, `tw-mgmt`. Each
  host added with `tw host add` (and the host of `tw up --separate-host`)
  is one more.

An object's node stays on the tailnet for as long as the object lives. It
does not go away when the object is idle (workerd evicts an idle object
from memory; its node stays online). It goes away when the object is
killed (`tw kill`), when its `ttl` runs out, or when its workload is
undeployed; a pool node goes away after an hour without new objects of its
class.

To spend fewer nodes, give fewer classes their own:
[recipes.md](recipes.md#choose-which-classes-get-their-own-node) says how
to choose. A class without a node must say `"node": false`. A project with
no `tailworks.jsonc` gets that for every class
([wrangler.md](wrangler.md#without-a-tailworksjsonc)). A class that has a
binding in the wrangler config but no entry in `tailnet.objects` is not a
way to say it: mgmt mints no node for it and answers
[`unknown_namespace`](errors.md#unknown_namespace) when the host asks for
one.

**Where to see it.** `tw policy` and `tw deploy`'s preflight print what
the app uses and what the tenant has now:

```
✓ preflight  nodes: this app is 1 workload node + 1 node per Room object (Counter objects share the workload node); tenant example.ts.net has 14 live nodes: 2 workloads, 11 objects, 1 pool (maxObjects 1000; host runs 14 of 500 node capacity)
```

`tw ls` prints the tenant's count in its `NODES` line, and `tw doctor` in
its `node_budget` check. With `--json`, all of them carry it in `nodes`
([cli-json.md](cli-json.md#node-budget-nodes)). The count is the tenant's
live objects in mgmt, including nodes that are still joining; tailworks'
own nodes are not in it.

tailworks has two limits of its own on the same count, and these it does
check:

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Live nodes per tenant (every row of the first table, previews and pool nodes) | 1000; past it a new object is refused 429 [`object_quota_exceeded`](errors.md#object_quota_exceeded) | `tw tenant set --max-objects` | `mgmt/types.go:DefaultMaxObjects` |
| `tw policy`, `tw deploy`, `tw ls` and `tw doctor` warn when the tenant's nodes are this close to that quota or to its hosts' node capacity | 80% | no | `cmd/tw/nodebudget.go:nodeBudgetWarnPct` |

The second limit is the node capacity each host advertises
(`tw-hostd -capacity-nodes`, in [Hosts](#hosts-and-the-microvm-tier)).
Placement uses it: a host with no free node capacity takes no new
workloads. It does not refuse new objects of the workloads a host already
runs. The warning compares the sum of the live hosts' capacity with the
larger of the tenant's count and the nodes those hosts report (which
covers every tenant they serve). Only an admin can read host capacity, so
a linked developer machine gets the `maxObjects` warning alone.

## Platform store reads while mgmt is slow

KV, D1 and R2 (ADR-044 §8). Details: [kv.md](kv.md), [d1.md](d1.md).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| A read's wait for mgmt before it serves the host's copy | 2 s | no | `hostd/gen/js/store.js:READ_WAIT_MS` |
| Background catch-up while mgmt is unreachable (reads don't wait) | at most every 5 s | no | `hostd/gen/js/store.js:STALE_RETRY_MS` |
| One journal read from mgmt (hostd) | 10 s | no | `hostd/stores.go:storeReadTimeout` |

## KV

Details: [kv.md](kv.md#limits).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Key | 1 to 512 bytes | no | `internal/kvwire/kvwire.go:MaxKeyBytes` |
| Value | 2 MiB − 4 KiB | no | `internal/kvwire/kvwire.go:MaxValueBytes` |
| Metadata | 1024 bytes | no | `internal/kvwire/kvwire.go:MaxMetadataBytes` |
| `expirationTtl` / `expiration` | at least 60 s ahead | no | `internal/kvwire/kvwire.go:MinTTLSeconds` |
| `cacheTtl` | at least 30 s; default freshness 60 s | per read | `hostd/gen/js/kv.js:MIN_CACHE_TTL`, `hostd/gen/js/kv.js:DEFAULT_FRESH_MS` |
| `list` page | 1000 keys | no | `internal/kvwire/kvwire.go:MaxListKeys` |
| Bulk `get` | 100 keys | no | `internal/kvwire/kvwire.go:MaxBulkKeys` |
| Namespace (its journal in mgmt) | 1 GiB | no | `mgmt/stores.go:StoreMaxBytes` |

## D1

Details: [d1.md](d1.md#limits).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| SQL statement | 100,000 bytes | no | `hostd/gen/js/store-d1.js:D1_LIMITS.sqlBytes` |
| Bound parameters per statement | 100 | no | `hostd/gen/js/store-d1.js:D1_LIMITS.params` |
| String or BLOB value | 2,000,000 bytes | no | `hostd/gen/js/store-d1.js:D1_LIMITS.valueBytes` |
| Statements per request | 1000 | no | `hostd/gen/js/store-d1.js:D1_LIMITS.statements` |
| Result of one request, and `tw d1 export` | 16 MiB | no | `hostd/gen/js/store-d1.js:D1_LIMITS.resultBytes` |
| Database | 1 GiB | no | `hostd/gen/js/store-d1.js:D1_LIMITS.databaseBytes` |
| One write (statement or batch), encoded | 2 MiB | no | `hostd/gen/js/store-d1.js:D1_LIMITS.entryBytes` |
| Journal (every write, no compaction yet) | 1 GiB | no | `mgmt/stores.go:StoreMaxBytes` |
| `tw d1 execute` / migration body | 8 MiB | no | `mgmt/d1.go:D1BodyLimit` |
| Query duration | none | — | |

## R2

Details: [r2.md](r2.md#limits-and-differences).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Key | 1024 bytes | no | `hostd/gen/js/store-r2.js:MAX_KEY` |
| Custom metadata | 8 KiB | no | `hostd/gen/js/store-r2.js:MAX_META` |
| Object | 5 GiB − 5 MiB | no | `hostd/gen/js/store-r2.js:MAX_VALUE`, `hostd/r2/backend.go:MaxBlob` |
| `list` page | 1000 keys (100 with `include`) | no | `hostd/gen/js/store-r2.js:MAX_LIST`, `hostd/gen/js/store-r2.js:#r2-list-include` |
| Multipart part (all but the last) | at least 5 MiB | no | `hostd/gen/js/store-r2.js:MIN_PART` |
| Multipart parts | 10,000 | no | `hostd/gen/js/store-r2.js:MAX_PART_NUMBER` |
| Bucket index (its journal in mgmt; object bytes are not counted) | 1 GiB | no | `mgmt/stores.go:StoreMaxBytes` |

## Queues

Details: [queues.md](queues.md#delivery).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Message body | 128 KB (128,000 bytes) | no | `internal/queuecfg/queuecfg.go:MaxMessageBytes` |
| `sendBatch` | 100 messages, 256 KB | no | `internal/queuecfg/queuecfg.go:MaxBatchMessages`, `internal/queuecfg/queuecfg.go:MaxBatchBytes` |
| `delaySeconds`, `delivery_delay`, `retry_delay` | 0 to 43,200 s (12 h) | per call / binding | `internal/queuecfg/queuecfg.go:MaxDelaySeconds` |
| `max_batch_size` | 1 to 100, default 10 | consumer config | `internal/queuecfg/queuecfg.go:MaxBatchSize` |
| `max_batch_timeout` | 0 to 60 s, default 5 | consumer config | `internal/queuecfg/queuecfg.go:MaxBatchTimeoutSecs` |
| `max_retries` | 0 to 100, default 3 | consumer config | `internal/queuecfg/queuecfg.go:MaxRetries` |
| `max_concurrency` | 1 to 20, default 1 | consumer config | `internal/queuecfg/queuecfg.go:MaxConcurrency` |
| Queue size (stored bytes) | 1 GiB, then `queue_limit` | no | `mgmt/queues.go:QueueMaxBytes` |
| All of a tenant's queues together | unlimited, else `storage_quota_exceeded` ([below](#tenant-storage)) | `tw tenant set --max-queue-bytes` | `mgmt/sqlstore_quota.go:#tenant-queue-bytes` |
| One `queue()` run | 15 min wall time | no | `hostd/queues.go:DefaultQueueTimeout` |
| Claimed batch hidden (push consumer) | 60 s, extended while `queue()` runs | `tw-hostd -queue-visibility` | `hostd/queues.go:DefaultQueueVisibility` |
| HTTP pull visibility timeout | 1 s to 12 h, default 30 s | per call | `internal/queuecfg/queuecfg.go:MaxVisibilityMillis`, `mgmt/queues.go:queueDefaultVisibility` |
| HTTP pull wait | up to 20 s | per call | `mgmt/queues.go:queueMaxWait` |

## Tenant storage

What one tenant may keep in mgmt across all its workloads, on top of the
per-store and per-queue limits above. The quotas are unlimited (0) unless
an operator sets them, so an upgrade never starts refusing a tenant's
writes. `tw tenant ls` shows each tenant's usage against them (`GET
/v1/tenants/{id}/usage`); past a quota a write fails 429
[`storage_quota_exceeded`](errors.md#storage_quota_exceeded). An append
that doesn't grow its store (an overwrite, a delete) is never refused, so a
full tenant can free space. On Postgres, concurrent writes to different
stores can take a tenant over by at most one in-flight write each.

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| All of a tenant's KV, D1 and R2 journals together | unlimited (0) | `tw tenant set --max-store-bytes` (`20GiB`, `-` = unlimited) | `mgmt/sqlstore_quota.go:#tenant-store-bytes`, `cmd/tw/tenant_quota.go:-max-store-bytes` |
| All of a tenant's queued messages together | unlimited (0) | `tw tenant set --max-queue-bytes` | `mgmt/sqlstore_quota.go:#tenant-queue-bytes`, `cmd/tw/tenant_quota.go:-max-queue-bytes` |
| Platform stores (KV namespaces, D1 databases, R2 buckets) per tenant | unlimited (0) | `tw tenant set --max-stores` | `mgmt/quota.go:#tenant-max-stores`, `cmd/tw/tenant_quota.go:-max-stores` |
| Largest stores and queues listed per tenant | 5 | no | `mgmt/quota.go:UsageLargestN` |
| `tw doctor` `store_sizes`: warn when a store, queue or tenant quota is this full | 80% | no | `cmd/tw/doctor_storage.go:doctorStoreWarnPct` |
| `tw doctor` `data_disk`: free space on the state dir's volume | warn under 5 GiB or 10%, fail under 1 GiB | no | `cmd/tw/doctor_storage.go:doctorDiskWarn`, `cmd/tw/doctor_storage.go:doctorDiskWarnPct`, `cmd/tw/doctor_storage.go:doctorDiskFail` |

## Cron triggers

Details: [cron-triggers.md](cron-triggers.md#syntax).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Triggers per workload | 10 | no | `internal/cron/cron.go:MaxTriggers` |
| Expression length | 128 bytes | no | `internal/cron/cron.go:MaxLen` |
| One `scheduled()` run | 15 min wall time, `ctx.waitUntil()` work included; the run is cancelled at the limit | no | `hostd/cron.go:DefaultCronTimeout` |
| Concurrent runs of one trigger | 1: a run that comes due while the previous one is in flight is skipped | no | `hostd/cron.go:#cron-no-overlap` |
| Retries of a failed run | none | — | [cron-triggers.md](cron-triggers.md#delivery) |

## Secrets

Details: [secrets.md](secrets.md#commands).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Secrets per workload | 128 | no | `mgmt/secrets.go:SecretMaxPerWorkload` |
| Value | 5 KiB | no | `mgmt/secrets.go:SecretMaxValueBytes` |
| Name | 1 to 128 characters, `[A-Za-z_][A-Za-z0-9_]*` | no | `mgmt/secrets.go:secretNameRE` |

## Public ingress

Requests from the internet (Funnel) only. Each limit has a default and a
ceiling; a workload sets its own up to the ceiling in `tailworks.jsonc`
`public.limits`. Details: [public-ingress.md](public-ingress.md#limits).

| Limit | Default | Ceiling | Source |
|---|---|---|---|
| `rps` / `burst` per workload | 50 / 100 | 1000 / 2000 | `internal/publicspec/publicspec.go:Defaults.RPS`, `internal/publicspec/publicspec.go:Ceilings.RPS`, `internal/publicspec/publicspec.go:Defaults.Burst`, `internal/publicspec/publicspec.go:Ceilings.Burst` |
| `ipRps` / `ipBurst` per client IP | 10 / 20 | 200 / 400 | `internal/publicspec/publicspec.go:Defaults.IPRPS`, `internal/publicspec/publicspec.go:Ceilings.IPRPS`, `internal/publicspec/publicspec.go:Defaults.IPBurst`, `internal/publicspec/publicspec.go:Ceilings.IPBurst` |
| `maxBodyBytes` | 1 MiB | 32 MiB | `internal/publicspec/publicspec.go:Defaults.MaxBodyBytes`, `internal/publicspec/publicspec.go:Ceilings.MaxBodyBytes` |
| `maxHeaderBytes` | 32 KiB | 64 KiB | `internal/publicspec/publicspec.go:Defaults.MaxHeaderBytes`, `internal/publicspec/publicspec.go:Ceilings.MaxHeaderBytes` |
| `timeoutSeconds` (whole request; not upgrades) | 30 | 300 | `internal/publicspec/publicspec.go:Defaults.TimeoutSeconds`, `internal/publicspec/publicspec.go:Ceilings.TimeoutSeconds`, `netd/public_serve.go:#public-timeout` |
| `maxConns` | 256 | 4096 | `internal/publicspec/publicspec.go:Defaults.MaxConns`, `internal/publicspec/publicspec.go:Ceilings.MaxConns` |

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Public routes per workload | 64 | no | `internal/publicspec/publicspec.go:MaxRoutes` |
| `public.maxObjects` per class | at most 1000 | `tailworks.jsonc` | `internal/publicspec/publicspec.go:MaxObjectsCeiling` |
| Objects created by public requests | 20 per minute per tenant | no | `internal/publicspec/publicspec.go:CreatesPerMinute` |
| Time to send request headers | 10 s | no | `netd/public_serve.go:ReadHeaderTimeout` |
| Idle keep-alive connection | 60 s | no | `netd/public_serve.go:IdleTimeout` |

## Logs and observability

Details: [observability.md](observability.md#volume-bounds).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| `console.*` lines per request record | 100, 4 KiB each, 16 KiB in all | no | `internal/obs/record.go:MaxLogLines`, `internal/obs/record.go:MaxLogLineBytes`, `internal/obs/record.go:MaxLogBytes` |
| Exceptions per record | 8, stack 4 KiB each | no | `internal/obs/record.go:MaxExceptions`, `internal/obs/record.go:MaxStackBytes` |
| Records written per workload | 200/s, bursts of 1000 | no | `hostd/telemetry.go:RecordRate`, `hostd/telemetry.go:RecordBurst` |
| Request log rotation | 8 MiB, one previous file kept | no | `hostd/telemetry.go:RecordFileMax` |
| `workerd.log` / `app.log` rotation | 16 MiB, one previous file kept | no | `hostd/telemetry.go:AppLogMax` |
| Ingress and workerd halves of a record joined within | 2 s | no | `hostd/telemetry.go:JoinWait` |
| OTLP export queue per signal and workload | 4096 items | no | `hostd/telemetry.go:QueueMax` |
| OTLP export interval: logs and spans / metrics | 2 s / 30 s | no | `hostd/telemetry.go:ExportEvery`, `hostd/telemetry.go:MetricsEvery` |
| OTLP headers / header value / endpoint | 16 / 4 KiB / 2048 bytes | no | `mgmt/telemetry.go:TelemetryMaxHeaders`, `mgmt/telemetry.go:TelemetryMaxHeaderValue`, `mgmt/telemetry.go:TelemetryMaxEndpoint` |
| `tw logs` lines | default 50, at most 10,000 | `tw logs -n` | `cmd/tw/ops.go:#logs-n`, `mgmt/logs.go:MaxLogLines` |
| `tw audit` records (bare command: the newest) | default 50, fetched in pages of 500 | `tw audit -n` (an object, `--human`, `--trace` and `--all` are always complete) | `cmd/tw/ops.go:#audit-n` |
| One `tw logs -f` stream | 1 h (the CLI reconnects) | no | `hostd/logs.go:maxLogFollow` |
| Request record max age | none (0): only the rotation above | `tw up --request-log-max-age`, `tw-hostd -request-log-max-age` (e.g. `168h`) | `cmd/tw-hostd/main.go:-request-log-max-age`, `cmd/tw/up_retention.go:-request-log-max-age` |
| Request record max age applied every | 10 min (or the max age, if shorter) | no | `hostd/telemetry_retention.go:RecordPruneEvery` |
| `tw up` daemon logs (`mgmt.log`, `hostd.log`) rotation | 32 MiB, one previous file kept | no | `cmd/tw/up_logs.go:daemonLogMax` |
| Console log (`workerd.log`, `app.log`) retention | none beyond the rotation above | — | |
| Audit records | kept forever (0) | `tw up --audit-max-age`, `--audit-max-records`; `tw-mgmt -audit-max-age`, `-audit-max-records` ([audit retention](#audit-retention)) | `cmd/tw-mgmt/main.go:-audit-max-age`, `cmd/tw-mgmt/main.go:-audit-max-records` |
| Audit retention applied every | 1 h (and at start), 5000 records per transaction | no | `mgmt/retention.go:AuditPruneInterval`, `mgmt/retention.go:auditPruneBatch` |

### Audit retention

Off by default: the audit log is the record of who did what and is
hash-chained, so deleting from it is opt-in. With a max age or a max record
count, mgmt deletes each chain's oldest records and keeps a checkpoint, the
seq and hash of the last one it deleted, in the same transaction. `tw audit`
verification starts from the checkpoint, so the records that remain still
verify, and an edit, gap or truncated tail among them is still caught
(`prunedThrough` in `GET /v1/audit/verification`). Every prune is itself an
audit record (`audit_pruned`). The store's append-only guard still refuses
deleting any record the checkpoint doesn't cover. The age rule stops at the
first record that isn't old enough, so a record with a skewed timestamp is
kept along with every record after it.

## Workflows and approvals

Workflows run on workerd's built-in engine; tailworks adds no limits of its own
to workflows, steps or retention. Details:
[approvals.md](approvals.md#waiting-for-an-approval).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Approval wait | 24 h by default, then rejected | `waitForApproval(step, {timeout})` | `js/packages/tailnet/src/workflows.ts:#approval-timeout` |
| Approval comment | 1024 bytes (longer is cut) | no | `hostd/approvals.go:maxApprovalComment` |
| Decided approvals listed per host | 1000 (the audit log keeps all) | no | `hostd/approvals.go:maxDecidedApprovals` |

## Containers

Details: [containers.md](containers.md#instance-types) and
[containers.md](containers.md#lifetime).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Instance types | `lite` (1/16 vCPU, 256 MiB, 2 GB) to `standard-4` (4 vCPU, 12 GiB, 20 GB) | `instance_type` | `internal/container/container.go:InstanceTypes.lite`, `internal/container/container.go:InstanceTypes.standard-4` |
| Custom instance | up to 4 vCPU, 12 GiB, 20 GB; at least 128 MiB | `instance_type` | `hostd/sandbox/container.go:MaxVCPU`, `hostd/sandbox/container.go:MaxMemoryMiB`, `hostd/sandbox/container.go:MaxDiskMB`, `hostd/sandbox/container.go:MinMemoryMiB` |
| Running containers per tenant | 10 | `tw tenant set --max-containers` | `mgmt/types.go:DefaultMaxContainers` |
| Container memory per tenant | 16,384 MiB | `tw tenant set --max-container-mem-mib` | `mgmt/types.go:DefaultMaxContainerMemMiB` |
| Container vCPU per tenant | 8 | `tw tenant set --max-container-vcpu` | `mgmt/types.go:DefaultMaxContainerVCPU` |
| Containers, memory, vCPU per host | unlimited | `tw-hostd -container-max`, `-container-mem-mib`, `-container-vcpu` | `cmd/tw-hostd/main.go:-container-max`, `cmd/tw-hostd/main.go:-container-mem-mib`, `cmd/tw-hostd/main.go:-container-vcpu` |
| Inactivity timeout | 10 min default, at most 6 h | `tw-hostd -container-idle`, `setInactivityTimeout` | `hostd/containers.go:DefaultContainerIdle`, `hostd/containers.go:MaxContainerIdle` |
| Hard max lifetime | 24 h | `tw-hostd -container-max-lifetime` | `hostd/containers.go:DefaultContainerMaxLife` |
| `destroy()` grace before SIGKILL | 10 s | `tw-hostd -container-stop-grace` | `hostd/containers.go:DefaultContainerGrace` |
| Quota reservation at `start()` | 10 s | no | `hostd/container_admit.go:QuotaTimeout` |
| Outbound intercepts per container | 128 | no | `netd/sbxnet/sbxnet.go:MaxIntercepts` |
| Image, uncompressed | 40 GiB | `tw-hostd -container-image-max-mib` | `hostd/image/limits.go:DefaultMaxImageBytes` |
| Image layers / entries / path depth / path length | 128 / 1 Mi / 256 / 4096 bytes | no | `hostd/image/limits.go:MaxLayers`, `hostd/image/limits.go:MaxEntries`, `hostd/image/limits.go:MaxDepth`, `hostd/image/limits.go:MaxName` |
| Converted image cache | 20 GiB (least recently used images not in use are removed past it; images in use stay, even past it) | `tw-hostd -container-image-cache-max-mib` (0 = no cap) | `internal/hostdrun/hostdrun.go:DefaultImageCacheMiB`, `cmd/tw-hostd/main.go:-container-image-cache-max-mib` |
| `exec()` duration | none | — | |

## Hosts and the microVM tier

Flags are `tw-hostd`'s; `tw up` uses its own defaults for some (1 s
heartbeat, 12 s kill tombstone).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Guest memory (microVM DO) | 512 MiB | `-vm-mem-mib` | `cmd/tw-hostd/main.go:-vm-mem-mib` |
| Guest vCPUs | 2 | `-vm-vcpus` | `cmd/tw-hostd/main.go:-vm-vcpus` |
| microVMs per host | 64 | no | `hostd/capacity.go:DefaultMaxVMs` |
| Host RAM offered to guests | 75% | no | `hostd/capacity.go:defaultVMMemShare` |
| Free disk offered as container scratch | 90% | no | `hostd/capacity.go:defaultScratchShare` |
| Warm VM pool | auto (at most 2) | `-vm-pool` | `cmd/tw-hostd/main.go:-vm-pool` |
| microVM boot | 60 s | no | `hostd/workload.go:#vm-boot` |
| Tailnet nodes advertised per host | 25% of RAM at 8 MiB per node, at least 50 and at most 1000 (500 when RAM can't be read); see [Per-node memory](#per-node-memory-measured) | `-capacity-nodes` (0: derived) | `hostd/capacitynodes.go:nodeMemShare`, `hostd/capacitynodes.go:NodeMemMiB`, `hostd/capacitynodes.go:MinCapacityNodes`, `hostd/capacitynodes.go:MaxCapacityNodes`, `hostd/capacitynodes.go:FallbackCapacityNodes` |
| Pre-joined nodes per hot class | 1 | `-pool-nodes` | `internal/hostdrun/hostdrun.go:DefaultPoolNodes` |
| Concurrent node joins | 16 | no | `netd/registry.go:JoinParallel` |
| One node join | 2 min | no | `netd/registry.go:JoinTimeout` |
| Heartbeat | 3 s | `-heartbeat` | `cmd/tw-hostd/main.go:-heartbeat` |
| Stop serving stateful workloads without a heartbeat | 30 s, at most 40 s | `-fence-after` | `hostd/host.go:DefaultFenceAfter`, `hostd/host.go:MaxFenceAfter` |
| Namespace lease (a DO's host) | 45 s | no | `mgmt/types.go:NamespaceLeaseTTL` |
| Host lease | 10 s | no | `mgmt/types.go:HostLeaseTTL` |
| Killed node keeps refusing connections | 10 s | `-kill-tombstone` | `cmd/tw-hostd/main.go:-kill-tombstone` |
| Node-runtime restart backoff | 250 ms doubling to 10 s, never gives up | no | `hostd/node.go:#node-restart-backoff` |
| Node-runtime stop: SIGTERM to SIGKILL | 3 s | no | `hostd/node.go:#node-stop-grace` |
| Warm keys per (tenant, tag set) | 5 | `tw-mgmt -warm-keys` | `internal/mgmtd/mgmtd.go:DefaultWarmKeys` |
| Warm key lifetime | 30 min | `tw-mgmt -warm-key-ttl` | `mgmt/warmkeys.go:DefaultWarmKeyTTL` |

### Per-node memory (measured)

What one object node costs a host: the resident memory (RSS) of one
process holding N netd object nodes, minus the same process before its
first join, divided by N. "Idle" is 15 s after the last join; "load" is the
peak while 32 concurrent callers send keep-alive HTTP requests round-robin
to every node for 10 s, through netd's ingress (WhoIs, attribution) to an
upstream that answers `ok`. Hermetic: testcontrol, DERP and STUN on
loopback in another process, so they aren't counted.

**Scoped** visibility is each object seeing only its callers, as grants
that keep objects from seeing one another give (RFC §9). **Mesh** is
every node seeing every other, the worst case: each node's netmap holds N
peers, so the total grows as N².

| Host | Peers | N=10 idle / load | N=100 idle / load | N=500 idle / load |
|---|---|---|---|---|
| Linux arm64 (Ubuntu 24.04, 8 vCPU VM on Apple M5 Pro) | scoped | 3.4 / 8.8 MB | 2.3 / 4.6 MB | 2.7 / 3.9 MB |
| Linux amd64 (Ubuntu 24.04, 2 vCPU KVM guest, AMD Ryzen R1600) | scoped | 2.8 / 5.8 MB | 2.1 / 3.0 MB | 2.5 / 2.5 MB¹ |
| macOS arm64 (Apple M5 Pro) | scoped | 4.4 / 9.8 MB | 2.3 / 5.3 MB | 2.4 / 4.1 MB |
| Linux arm64 | mesh | 4.1 / 9.9 MB | 4.7 / 7.3 MB | did not finish² |
| Linux amd64 | mesh | 3.5 / 5.9 MB | 6.1 / 6.2 MB | not run |
| macOS arm64 | mesh | 5.7 / 9.9 MB | 12.2 / 12.2 MB | 32.6 / 33.1 MB |

All per node. The process before its first join is 21–23 MB. At N=10 the
load column is mostly fixed per-process buffers spread over few nodes.
tsnet runs 73 goroutines per node on Linux arm64, 49 on the amd64 guest
and 113 on macOS. With scoped peers, a host serves 1,600–2,300 requests
per second at N=500 (arm64; one netd process, the caller on the same
machine), with p50 2.7–4.8 ms. Zero requests failed in any run.

¹ The 2-vCPU guest was CPU-bound at N=500 (36 requests per second), so the
load figure there is not a load figure.
² At mesh N=500 on Linux, tsnet's LocalBackend watchdog panicked the
process (`ipnlocal: watchdog timeout`) at about 10 GB during the joins. On
macOS the same run held 16 GB and served 4 requests per second. Don't give
hundreds of objects on one host visibility of one another.

The default node capacity budgets **8 MiB per node**: twice the scoped
figure under load, which leaves room for some visible peers. It offers
object nodes 25% of the host's RAM, so a 16 GiB host advertises 512 nodes
and an 8 GiB host 256. Hosts whose objects see one another (mesh) need a
lower `-capacity-nodes`, from the mesh rows. The 1000 ceiling isn't a
memory limit: past it, a restart's rejoin and netmap size are.

Reproduce with `make bench` (`BenchmarkRegistryNodes` in
`netd/bench_test.go`; `BENCH_NODES='10|100'` for fewer sizes). For a Linux
VM or another machine, build the test binary with
`GOOS=linux GOARCH=arm64 go test -c -o netd.test ./netd/` and run
`./netd.test -test.run '^$' -test.bench RegistryNodes -test.benchtime=1x`.
Measured 2026-11-21 at the commit that added this section. The macOS runs
shared the machine with other work, so its request rates are low bounds.

### Registration rate (measured)

How fast one host registers fresh object nodes with Tailscale's production
control: up to 50 fresh, ephemeral nodes from one netd registry, on a test
tailnet, `make e2e-regrate-real` (`e2e/regrate`).

| Keys | Concurrent joins | 50 nodes ready in | Rate | Join p50 / max | Register request p50 / max |
|---|---|---|---|---|---|
| one reusable key | 16 (netd's default) | 5.8 s | 513 per min | 1.50 / 1.71 s | 0.43 / 0.46 s |
| one single-use key per object, minted in the join | 16 | 6.3 s | 479 per min | 1.38 / 1.53 s | 0.43 / 0.45 s |
| one reusable key | 50 | 2.6 s | 1,148 per min | 2.00 / 2.58 s | 0.43 / 0.44 s |

No registration was refused or slowed: the register round trip stayed at
0.43 s at 50-way. At these sizes the rate is concurrent joins ÷ join time,
set by netd's concurrent-join limit (`JoinParallel`), not by control. Sustained rates over more
than 50 nodes, and several hosts registering at once, were not measured.

## Management API

The contract is [contracts/mgmt-api.md](contracts/mgmt-api.md). The rate
and concurrency limits apply to tailnet callers; mgmt's own node (the
operator console) and the local socket are not limited.

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Request body (admin, including `POST /v1/deployments`) | 64 MiB | no | `mgmt/server.go:#mgmt-body` |
| Request body from a host | 1 MiB (store appends 6 MiB, audit batches 64 MiB) | no | `mgmt/v1.go:hostBodyLimit` |
| Request rate per tailnet caller (WhoIs node; not hosts), then `rate_limited` | 100 requests/s, bursts of 200 | `tw-mgmt -api-rps` (-1: off) | `mgmt/ratelimit.go:DefaultAPIRPS` |
| Request rate per host | 1000 requests/s, bursts of 2000 | `tw-mgmt -api-host-rps` (-1: off) | `mgmt/ratelimit.go:DefaultAPIHostRPS` |
| Concurrent requests from non-host tailnet callers, all together (hosts are not counted) | 256, then `rate_limited` | `tw-mgmt -api-inflight` (-1: off) | `mgmt/ratelimit.go:DefaultAPIInFlight` |
| List page | default 50, at most 500 | `limit` query parameter | `mgmt/page.go:PageDefaultLimit`, `mgmt/page.go:PageMaxLimit` |
| D1 SQL body | 8 MiB | no | `mgmt/d1.go:D1BodyLimit` |
| Upgrade artifact upload | 2 GiB | no | `mgmt/v1_upgrades.go:maxArtifactBytes` |
