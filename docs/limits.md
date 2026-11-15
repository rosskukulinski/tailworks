# Limits

Every size, count, rate and timeout twd enforces, in one place, with the
line of code that sets it. Limits apply per host and per workload unless a
row says otherwise (a tenant quota covers all of a tenant's workloads).
**Configurable** names the flag or config key that changes a limit; "no"
means it is a constant. Several limits copy Cloudflare's so that code
written for Workers behaves the same; the feature pages explain the
differences. [Limits that don't exist](#limits-that-dont-exist) lists what
twd deliberately does not bound.

## Limits that don't exist

These were checked in the workerd config twd generates (`hostd/gen`) and in
the proxies a tailnet request passes through (netd, then hostd, then workerd).

| Limit | Value | Notes |
|---|---|---|
| CPU time per request, alarm, cron or queue run | none | the workerd config sets no CPU limit; wrangler's `limits` key is ignored (`cmd/twd/wranglercfg.go:249`) |
| Memory per isolate | none set by twd | V8's own heap limit applies; Durable Objects on the [microVM tier](#hosts-and-the-microvm-tier) are bounded by the guest's memory |
| Wall time of a tailnet request | none | `netd/ingress.go:183` and the hostd ingress set no deadline. Requests from the internet do have one: see [Public ingress](#public-ingress) |
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
| Deploy request body (the bundle, JSON-encoded) | 64 MiB | no | `mgmt/server.go:180` |
| Unpacked bundle, all files | 64 MiB | no | `hostd/bundle/bundle.go:27` |
| Bundle sent into a microVM guest | 256 MiB | no | `guest/twd-guestd/main_linux.go:313` |
| `twd deploy` wait for ready | 3 min | `twd deploy --wait` | `cmd/twd/app.go:331` |
| One host bringing up a deployment | 2 min | no | `hostd/workload.go:182` |
| In-flight requests on the old instance at a redeploy (then it stops) | 10 s | no | `hostd/host.go:359` |
| Replicas of a stateless workload | 16 | `twd.jsonc` `replicas` | `mgmt/workload_replicas.go:30` |

## Requests

See [Limits that don't exist](#limits-that-dont-exist) for duration, body and CPU.

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Request headers on an object's tailnet ingress | 1 MiB (Go's default) | no | `netd/registry.go:532` |
| Time to send request headers | 30 s | no | `netd/registry.go:534` |
| Caller lookup (WhoIs) per request | 5 s, then `no_identity` | no | `netd/ingress.go:156` |
| Wait for a running instance during a redeploy | 15 s, then `upstream_unavailable` | no | `hostd/http.go:87` |
| Wait for an object's node (`identity_pending` after) | 30 s | no | `hostd/host.go:353` |
| `Retry-After` on `identity_pending` | 1 s | no | `mgmt/server.go:170` |
| Node-tier caller header clock skew | 60 s | `maxSkewMs` in `@twd/node` | `js/packages/node/src/index.ts:37` |

## Durable Objects

See also [create-admission.md](create-admission.md).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Live objects per tenant | 1000 | `twd tenant set --max-objects` | `mgmt/types.go:42` |
| Spawn depth below a root object | 3 (a deployment may only lower it) | `twd.jsonc` `tailnet.spawn.maxDepth` | `mgmt/types.go:47`, `mgmt/service.go:947` |
| Live descendants of one root object | 64 (a deployment may only lower it) | `twd.jsonc` `tailnet.spawn.maxLive` | `mgmt/types.go:46`, `mgmt/service.go:944` |
| Node registrations per tenant | 200 per minute (twd's own throttle) | `twd tenant set --registrations-per-minute` | `mgmt/types.go:41` |
| Create ticket lifetime | 2 min | no | `hostd/gen/mac.go:143` |
| `ready()` wait after a spawn | `timeoutMs`, at most 5 min (else 30 s) | per call | `hostd/http.go:422` |
| Node join after a create | 3 min | no | `hostd/objects.go:366` |
| Retries of a failed node join | 5 (1, 2, 4, 8, 16 s) | no | `hostd/objects.go:440` |
| Storage per object (workerd tier) | none | — | |
| Storage per object (microVM tier) | 1024 MiB volume (sparse) | `twd-hostd -vm-volume-mib` | `cmd/twd-hostd/main.go:89` |

## KV

Details: [kv.md](kv.md#limits).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Key | 1 to 512 bytes | no | `internal/kvwire/kvwire.go:36` |
| Value | 2 MiB − 4 KiB | no | `internal/kvwire/kvwire.go:38` |
| Metadata | 1024 bytes | no | `internal/kvwire/kvwire.go:37` |
| `expirationTtl` / `expiration` | at least 60 s ahead | no | `internal/kvwire/kvwire.go:43` |
| `cacheTtl` | at least 30 s; default freshness 60 s | per read | `hostd/gen/js/kv.js:32` |
| `list` page | 1000 keys | no | `internal/kvwire/kvwire.go:39` |
| Bulk `get` | 100 keys | no | `internal/kvwire/kvwire.go:40` |
| Namespace (its journal in mgmt) | 1 GiB | no | `mgmt/stores.go:50` |

## D1

Details: [d1.md](d1.md#limits).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| SQL statement | 100,000 bytes | no | `hostd/gen/js/store-d1.js:31` |
| Bound parameters per statement | 100 | no | `hostd/gen/js/store-d1.js:32` |
| String or BLOB value | 2,000,000 bytes | no | `hostd/gen/js/store-d1.js:33` |
| Statements per request | 1000 | no | `hostd/gen/js/store-d1.js:34` |
| Result of one request, and `twd d1 export` | 16 MiB | no | `hostd/gen/js/store-d1.js:35` |
| Database | 1 GiB | no | `hostd/gen/js/store-d1.js:36` |
| One write (statement or batch), encoded | 2 MiB | no | `hostd/gen/js/store-d1.js:37` |
| Journal (every write, no compaction yet) | 1 GiB | no | `mgmt/stores.go:50` |
| `twd d1 execute` / migration body | 8 MiB | no | `mgmt/d1.go:29` |
| Query duration | none | — | |

## R2

Details: [r2.md](r2.md#limits-and-differences).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Key | 1024 bytes | no | `hostd/gen/js/store-r2.js:27` |
| Custom metadata | 8 KiB | no | `hostd/gen/js/store-r2.js:28` |
| Object | 5 GiB − 5 MiB | no | `hostd/gen/js/store-r2.js:29`, `hostd/r2/backend.go:45` |
| `list` page | 1000 keys (100 with `include`) | no | `hostd/gen/js/store-r2.js:30`, `hostd/gen/js/store-r2.js:400` |
| Multipart part (all but the last) | at least 5 MiB | no | `hostd/gen/js/store-r2.js:31` |
| Multipart parts | 10,000 | no | `hostd/gen/js/store-r2.js:32` |
| Bucket index (its journal in mgmt; object bytes are not counted) | 1 GiB | no | `mgmt/stores.go:50` |

## Queues

Details: [queues.md](queues.md#delivery).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Message body | 128 KB (128,000 bytes) | no | `internal/queuecfg/queuecfg.go:17` |
| `sendBatch` | 100 messages, 256 KB | no | `internal/queuecfg/queuecfg.go:18` |
| `delaySeconds`, `delivery_delay`, `retry_delay` | 0 to 43,200 s (12 h) | per call / binding | `internal/queuecfg/queuecfg.go:20` |
| `max_batch_size` | 1 to 100, default 10 | consumer config | `internal/queuecfg/queuecfg.go:21` |
| `max_batch_timeout` | 0 to 60 s, default 5 | consumer config | `internal/queuecfg/queuecfg.go:22` |
| `max_retries` | 0 to 100, default 3 | consumer config | `internal/queuecfg/queuecfg.go:23` |
| `max_concurrency` | 1 to 20, default 1 | consumer config | `internal/queuecfg/queuecfg.go:24` |
| Queue size (stored bytes) | 1 GiB, then `queue_limit` | no | `mgmt/queues.go:41` |
| One `queue()` run | 15 min wall time | no | `hostd/queues.go:54` |
| Claimed batch hidden (push consumer) | 60 s, extended while `queue()` runs | `twd-hostd -queue-visibility` | `hostd/queues.go:51` |
| HTTP pull visibility timeout | 1 s to 12 h, default 30 s | per call | `internal/queuecfg/queuecfg.go:25`, `mgmt/queues.go:42` |
| HTTP pull wait | up to 20 s | per call | `mgmt/queues.go:43` |

## Cron triggers

Details: [cron-triggers.md](cron-triggers.md#syntax).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Triggers per workload | 10 | no | `internal/cron/cron.go:48` |
| Expression length | 128 bytes | no | `internal/cron/cron.go:45` |
| One `scheduled()` run | 15 min wall time | no | `hostd/cron.go:43` |
| Retries of a failed run | none | — | [cron-triggers.md](cron-triggers.md#delivery) |

## Secrets

Details: [secrets.md](secrets.md#commands).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Secrets per workload | 128 | no | `mgmt/secrets.go:29` |
| Value | 5 KiB | no | `mgmt/secrets.go:30` |
| Name | 1 to 128 characters, `[A-Za-z_][A-Za-z0-9_]*` | no | `mgmt/secrets.go:34` |

## Public ingress

Requests from the internet (Funnel) only. Each limit has a default and a
ceiling; a workload sets its own up to the ceiling in `twd.jsonc`
`public.limits`. Details: [public-ingress.md](public-ingress.md#limits).

| Limit | Default | Ceiling | Source |
|---|---|---|---|
| `rps` / `burst` per workload | 50 / 100 | 1000 / 2000 | `internal/publicspec/publicspec.go:68` |
| `ipRps` / `ipBurst` per client IP | 10 / 20 | 200 / 400 | `internal/publicspec/publicspec.go:68` |
| `maxBodyBytes` | 1 MiB | 32 MiB | `internal/publicspec/publicspec.go:68` |
| `maxHeaderBytes` | 32 KiB | 64 KiB | `internal/publicspec/publicspec.go:69` |
| `timeoutSeconds` (whole request; not upgrades) | 30 | 300 | `internal/publicspec/publicspec.go:69`, `netd/public_serve.go:659` |
| `maxConns` | 256 | 4096 | `internal/publicspec/publicspec.go:69` |

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Public routes per workload | 64 | no | `internal/publicspec/publicspec.go:32` |
| `public.maxObjects` per class | at most 1000 | `twd.jsonc` | `internal/publicspec/publicspec.go:36` |
| Objects created by public requests | 20 per minute per tenant | no | `internal/publicspec/publicspec.go:40` |
| Time to send request headers | 10 s | no | `netd/public_serve.go:296` |
| Idle keep-alive connection | 60 s | no | `netd/public_serve.go:297` |

## Logs and observability

Details: [observability.md](observability.md#volume-bounds).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| `console.*` lines per request record | 100, 4 KiB each, 16 KiB in all | no | `internal/obs/record.go:25` |
| Exceptions per record | 8, stack 4 KiB each | no | `internal/obs/record.go:28` |
| Records written per workload | 200/s, bursts of 1000 | no | `hostd/telemetry.go:67` |
| Request log rotation | 8 MiB, one previous file kept | no | `hostd/telemetry.go:73` |
| `workerd.log` / `app.log` rotation | 16 MiB, one previous file kept | no | `hostd/telemetry.go:76` |
| Ingress and workerd halves of a record joined within | 2 s | no | `hostd/telemetry.go:62` |
| OTLP export queue per signal and workload | 4096 items | no | `hostd/telemetry.go:79` |
| OTLP export interval: logs and spans / metrics | 2 s / 30 s | no | `hostd/telemetry.go:63` |
| OTLP headers / header value / endpoint | 16 / 4 KiB / 2048 bytes | no | `mgmt/telemetry.go:30` |
| `twd logs` lines | default 50, at most 10,000 | `twd logs -n` | `cmd/twd/ops.go:586`, `mgmt/logs.go:30` |
| One `twd logs -f` stream | 1 h (the CLI reconnects) | no | `hostd/logs.go:36` |
| Log retention | none beyond the rotation above | — | |

## Workflows and approvals

Workflows run on workerd's built-in engine; twd adds no limits of its own
to workflows, steps or retention. Details:
[approvals.md](approvals.md#waiting-for-an-approval).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Approval wait | 24 h by default, then rejected | `waitForApproval(step, {timeout})` | `js/packages/tailnet/src/workflows.ts:67` |
| Approval comment | 1024 bytes (longer is cut) | no | `hostd/approvals.go:314` |
| Decided approvals listed per host | 1000 (the audit log keeps all) | no | `hostd/approvals.go:64` |

## Containers

Details: [containers.md](containers.md#instance-types) and
[containers.md](containers.md#lifetime).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Instance types | `lite` (1/16 vCPU, 256 MiB, 2 GB) to `standard-4` (4 vCPU, 12 GiB, 20 GB) | `instance_type` | `internal/container/container.go:47` |
| Custom instance | up to 4 vCPU, 12 GiB, 20 GB; at least 128 MiB | `instance_type` | `hostd/sandbox/container.go:151` |
| Running containers per tenant | 10 | `twd tenant set --max-containers` | `mgmt/types.go:43` |
| Container memory per tenant | 16,384 MiB | `twd tenant set --max-container-mem-mib` | `mgmt/types.go:44` |
| Container vCPU per tenant | 8 | `twd tenant set --max-container-vcpu` | `mgmt/types.go:45` |
| Containers, memory, vCPU per host | unlimited | `twd-hostd -container-max`, `-container-mem-mib`, `-container-vcpu` | `cmd/twd-hostd/main.go:119` |
| Inactivity timeout | 10 min default, at most 6 h | `twd-hostd -container-idle`, `setInactivityTimeout` | `hostd/containers.go:49` |
| Hard max lifetime | 24 h | `twd-hostd -container-max-lifetime` | `hostd/containers.go:51` |
| `destroy()` grace before SIGKILL | 10 s | `twd-hostd -container-stop-grace` | `hostd/containers.go:52` |
| Quota reservation at `start()` | 10 s | no | `hostd/container_admit.go:59` |
| Outbound intercepts per container | 128 | no | `netd/sbxnet/sbxnet.go:53` |
| Image, uncompressed | 40 GiB | `twd-hostd -container-image-max-mib` | `hostd/image/limits.go:33` |
| Image layers / entries / path depth / path length | 128 / 1 Mi / 256 / 4096 bytes | no | `hostd/image/limits.go:39` |
| Converted image cache | unlimited | `twd-hostd -container-image-cache-max-mib` | `cmd/twd-hostd/main.go:115` |
| `exec()` duration | none | — | |

## Hosts and the microVM tier

Flags are `twd-hostd`'s; `twd up` uses its own defaults for some (1 s
heartbeat, 12 s kill tombstone).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Guest memory (microVM DO) | 512 MiB | `-vm-mem-mib` | `cmd/twd-hostd/main.go:90` |
| Guest vCPUs | 2 | `-vm-vcpus` | `cmd/twd-hostd/main.go:91` |
| microVMs per host | 64 | no | `hostd/capacity.go:16` |
| Host RAM offered to guests | 75% | no | `hostd/capacity.go:20` |
| Free disk offered as container scratch | 90% | no | `hostd/capacity.go:23` |
| Warm VM pool | auto (at most 2) | `-vm-pool` | `cmd/twd-hostd/main.go:98` |
| microVM boot | 60 s | no | `hostd/workload.go:848` |
| Tailnet nodes advertised per host | 1000 | `-capacity-nodes` | `cmd/twd-hostd/main.go:79` |
| Pre-joined nodes per hot class | 1 | `-pool-nodes` | `internal/hostdrun/hostdrun.go:47` |
| Concurrent node joins | 16 | `twd-netd -join-parallel` | `cmd/twd-netd/main.go:62` |
| One node join | 2 min | no | `netd/registry.go:167` |
| Heartbeat | 3 s | `-heartbeat` | `cmd/twd-hostd/main.go:74` |
| Stop serving stateful workloads without a heartbeat | 30 s, at most 40 s | `-fence-after` | `hostd/host.go:328` |
| Namespace lease (a DO's host) | 45 s | no | `mgmt/types.go:48` |
| Host lease | 10 s | no | `mgmt/types.go:49` |
| Killed node keeps refusing connections | 10 s | `-kill-tombstone` | `cmd/twd-hostd/main.go:80` |
| Node-tier restart backoff | 250 ms doubling to 10 s, never gives up | no | `hostd/node.go:301` |
| Node-tier stop: SIGTERM to SIGKILL | 3 s | no | `hostd/node.go:538` |
| Warm keys per (tenant, tag set) | 5 | `twd-mgmt -warm-keys` | `internal/mgmtd/mgmtd.go:44` |
| Warm key lifetime | 30 min | `twd-mgmt -warm-key-ttl` | `mgmt/warmkeys.go:38` |

## Management API

The contract is [contracts/mgmt-api.md](contracts/mgmt-api.md).

| Limit | Value | Configurable? | Source |
|---|---|---|---|
| Request body (admin, including `POST /v1/deployments`) | 64 MiB | no | `mgmt/server.go:180` |
| Request body from a host | 1 MiB (store appends 6 MiB, audit batches 64 MiB) | no | `mgmt/v1.go:1460` |
| List page | default 50, at most 500 | `limit` query parameter | `mgmt/page.go:17` |
| D1 SQL body | 8 MiB | no | `mgmt/d1.go:29` |
| Upgrade artifact upload | 2 GiB | no | `mgmt/v1_upgrades.go:81` |
| Request rate | none | — | |
