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

These were checked in the workerd config tailworks generates (`hostd/gen`) and in
the proxies a tailnet request passes through (netd, then hostd, then workerd).

| Limit | Value | Notes |
|---|---|---|
| CPU time per request, alarm, cron or queue run | none | the workerd config sets no CPU limit; wrangler's `limits` key is ignored (`cmd/tw/wranglercfg.go:#wrangler-limits-ignored`) |
| Memory per isolate | none set by tailworks | V8's own heap limit applies; Durable Objects on the [microVM tier](#hosts-and-the-microvm-tier) are bounded by the guest's memory |
| Wall time of a tailnet request | none | `netd/ingress.go:#tailnet-no-deadline` and the hostd ingress set no deadline. Requests from the internet do have one: see [Public ingress](#public-ingress) |
| Tailnet request body size | none | |
| Request rate over the tailnet | none | your tailnet policy decides who can connect |
| Outbound `fetch()` duration or count (subrequests) | none | |
| Concurrent requests per workload or object | none | |
| WebSocket duration | none | |
| Queue message retention | none | a message is kept until it is consumed; a full queue refuses new sends ([queues.md](queues.md#differences-from-cloudflare)) |
| Rate of mgmt API calls | none | |

## Deploy and bundles

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Deploy request body (the bundle, JSON-encoded) | 64 MiB | no | `mgmt/server.go:#mgmt-body` |
| Unpacked bundle, all files | 64 MiB | no | `hostd/bundle/bundle.go:MaxBundle` |
| Bundle sent into a microVM guest | 256 MiB | no | `guest/tw-guestd/main_linux.go:maxBundle` |
| `tw deploy` wait for ready | 3 min | `tw deploy --wait` | `cmd/tw/app.go:-wait` |
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
| Node-tier caller header clock skew | 60 s | `maxSkewMs` in `@tailworks/node` | `js/packages/node/src/index.ts:DEFAULT_MAX_SKEW_MS` |

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
| `tw d1 execute` / migration body | 8 MiB | no | `mgmt/d1.go:d1BodyLimit` |
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
| One `queue()` run | 15 min wall time | no | `hostd/queues.go:DefaultQueueTimeout` |
| Claimed batch hidden (push consumer) | 60 s, extended while `queue()` runs | `tw-hostd -queue-visibility` | `hostd/queues.go:DefaultQueueVisibility` |
| HTTP pull visibility timeout | 1 s to 12 h, default 30 s | per call | `internal/queuecfg/queuecfg.go:MaxVisibilityMillis`, `mgmt/queues.go:queueDefaultVisibility` |
| HTTP pull wait | up to 20 s | per call | `mgmt/queues.go:queueMaxWait` |

## Cron triggers

Details: [cron-triggers.md](cron-triggers.md#syntax).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Triggers per workload | 10 | no | `internal/cron/cron.go:MaxTriggers` |
| Expression length | 128 bytes | no | `internal/cron/cron.go:MaxLen` |
| One `scheduled()` run | 15 min wall time | no | `hostd/cron.go:DefaultCronTimeout` |
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
| One `tw logs -f` stream | 1 h (the CLI reconnects) | no | `hostd/logs.go:maxLogFollow` |
| Log retention | none beyond the rotation above | — | |

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
| Converted image cache | unlimited | `tw-hostd -container-image-cache-max-mib` | `cmd/tw-hostd/main.go:-container-image-cache-max-mib` |
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
| Tailnet nodes advertised per host | 1000 | `-capacity-nodes` | `cmd/tw-hostd/main.go:-capacity-nodes` |
| Pre-joined nodes per hot class | 1 | `-pool-nodes` | `internal/hostdrun/hostdrun.go:DefaultPoolNodes` |
| Concurrent node joins | 16 | `tw-netd -join-parallel` | `cmd/tw-netd/main.go:-join-parallel` |
| One node join | 2 min | no | `netd/registry.go:JoinTimeout` |
| Heartbeat | 3 s | `-heartbeat` | `cmd/tw-hostd/main.go:-heartbeat` |
| Stop serving stateful workloads without a heartbeat | 30 s, at most 40 s | `-fence-after` | `hostd/host.go:DefaultFenceAfter`, `hostd/host.go:MaxFenceAfter` |
| Namespace lease (a DO's host) | 45 s | no | `mgmt/types.go:NamespaceLeaseTTL` |
| Host lease | 10 s | no | `mgmt/types.go:HostLeaseTTL` |
| Killed node keeps refusing connections | 10 s | `-kill-tombstone` | `cmd/tw-hostd/main.go:-kill-tombstone` |
| Node-tier restart backoff | 250 ms doubling to 10 s, never gives up | no | `hostd/node.go:#node-restart-backoff` |
| Node-tier stop: SIGTERM to SIGKILL | 3 s | no | `hostd/node.go:#node-stop-grace` |
| Warm keys per (tenant, tag set) | 5 | `tw-mgmt -warm-keys` | `internal/mgmtd/mgmtd.go:DefaultWarmKeys` |
| Warm key lifetime | 30 min | `tw-mgmt -warm-key-ttl` | `mgmt/warmkeys.go:DefaultWarmKeyTTL` |

## Management API

The contract is [contracts/mgmt-api.md](contracts/mgmt-api.md).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Request body (admin, including `POST /v1/deployments`) | 64 MiB | no | `mgmt/server.go:#mgmt-body` |
| Request body from a host | 1 MiB (store appends 6 MiB, audit batches 64 MiB) | no | `mgmt/v1.go:hostBodyLimit` |
| List page | default 50, at most 500 | `limit` query parameter | `mgmt/page.go:PageDefaultLimit`, `mgmt/page.go:PageMaxLimit` |
| D1 SQL body | 8 MiB | no | `mgmt/d1.go:d1BodyLimit` |
| Upgrade artifact upload | 2 GiB | no | `mgmt/v1_upgrades.go:maxArtifactBytes` |
| Request rate | none | — | |
