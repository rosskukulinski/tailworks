# Logs, metrics and traces

tailworks records every request to your workloads and Durable Objects, keeps
metrics for them and for itself, and traces requests across tailnet hops.
It is built on open formats: Prometheus for scraping, and OpenTelemetry
(OTLP) for exporting logs, metrics and traces to a collector you run.

Nothing needs configuring for the first two:

- `tw logs <workload>` shows one structured line per request.
- Every host and mgmt serve `GET /metrics` on their own tailnet node.

Export to a collector is one command: `tw telemetry set`. The design is
[ADR-045](design/adr/045-observability.md).

## Request logs: `tw logs`

```console
$ tw logs support
2026-11-15T10:00:00.123Z GET /agents/support-agent/alice 200 14ms  SupportAgent/alice  caller=ross@example.com  trace=4bf92f3577b34da6a3ce929d0e0e4736
    log   loaded 3 messages
2026-11-15T10:00:02.511Z POST /hook 500 3.2ms exception  caller=public:203.0.113.9  trace=9a1c…
    error TypeError: Cannot read properties of undefined (reading 'id')
          at Object.fetch (src/index.ts:41:18)
2026-11-15T10:00:02.530Z fetch GET /alice ok  SupportAgent  trace=4bf92f35…
    warn  cache miss
```

Each record is one request through tailworks' ingress (`kind: request`), or one
invocation that had none (`kind: invocation`: a Durable Object called from
your Worker, an alarm, a cron run, an RPC call). A record holds:

- the time, tenant, workload and deployment;
- the object (tailworks object ID, class and name);
- the **verified caller**: the tailnet node, the user for a person's
  device, its tags, or `public` and the client IP for a Funnel request;
- the method, path, status and duration;
- the invocation's outcome (`ok`, `exception`, `canceled`, …);
- its `console.*` lines (with their level) and uncaught exceptions;
- the W3C trace and span IDs, and the audit trace ID (`tw audit --trace`).

Flags:

| Flag | |
|---|---|
| `-n 50` | the last 50 records (that match the filters) |
| `-f` | follow |
| `--json` | the records as JSON Lines (the `RequestRecord` schema in [api/openapi.yaml](../api/openapi.yaml)) |
| `--object <id \| Class/name \| Class \| name>` | one object, or one class |
| `--status 404`, `--status 5xx` | a status or a class of statuses |
| `--outcome exception` | a workerd outcome |
| `--errors` | status ≥ 400, an outcome other than `ok`, or an exception |
| `--caller <text>` | callers whose login or node name contains it (`public` for Funnel) |
| `--trace <id>` | one trace (a prefix works), or an audit trace ID |
| `--console` | the app's raw output instead: workerd's stdout, the node runtime's `app.log`, a microVM's console |

The filters run on the host, so `tw logs api --errors -n 20` is the last 20
errors, not the errors among the last 20 records. From a `tw link`ed
machine `tw logs` works the same (mgmt reads the host holding the
workload's lease).

A record never contains request or response bodies, query strings, request
headers, secret values or create tickets.

A request and its workerd invocation are reported separately and joined on
the host; a record is written when both halves are in, or 2 s after the
first. A Durable Object invocation with no ingress half (called from your
Worker) appears about 2 s after it ran.

The records are in `~/.tailworks/hostd/tenants/<tenant>/logs/<workload>.requests.jsonl`
on the host.

## Metrics: `GET /metrics`

hostd and mgmt serve the Prometheus text format on **port 9464 of their own
tailnet node**, and nowhere else. There is no token: the tailnet policy
decides who can scrape. Grant your Prometheus (here tagged `tag:prometheus`)
the port:

```jsonc
"grants": [
  {"src": ["tag:prometheus"], "dst": ["tag:tw-host", "tag:tw-mgmt"], "ip": ["tcp:9464"]},
],
```

Then scrape the nodes by MagicDNS name:

```yaml
scrape_configs:
  - job_name: tw
    static_configs:
      - targets: ["tw-mgmt.<tailnet>.ts.net:9464", "<host-id>.<tailnet>.ts.net:9464"]
```

`tw up` runs mgmt and the host in one process with one node, `tw-mgmt`,
which serves both sets of metrics. `-metrics-port` on `tw-hostd` and
`tw-mgmt` changes the port (`0` turns it off).

These are operator metrics: they cover every tenant on the host. A tenant
gets its own workloads' metrics through its OTLP export (below).

Names follow the OpenTelemetry semantic conventions where one exists, and
the standard OpenTelemetry → Prometheus translation (dots become `_`, the
unit becomes a suffix, counters end in `_total`).

### hostd

| OpenTelemetry name | Prometheus name | Type | Attributes | What |
|---|---|---|---|---|
| `http.server.request.duration` (s) | `http_server_request_duration_seconds` | histogram | `http.request.method`, `http.response.status_code`, `tw.tenant.id`, `tw.workload`, `tw.object.class` | every request to a workload or object, any tier, through hostd's ingress (requests, errors and latency) |
| `http.client.request.duration` (s) | `http_client_request_duration_seconds` | histogram | `http.request.method`, `http.response.status_code`, `error.type` (a refusal: `peer_not_visible`, `forbidden_destination`, …), `tw.tenant.id`, `tw.workload`, `tw.object.id` | egress, **per object** |
| `tw.invocations` | `tw_invocations_total` | counter | `tw.tenant.id`, `tw.workload`, `tw.object.class`, `tw.invocation.event` (`fetch`, `alarm`, `rpc`, `scheduled`, …), `tw.invocation.outcome` | workerd invocations (from its tail) |
| `tw.do.wakeups` | `tw_do_wakeups_total` | counter | `tw.tenant.id`, `tw.workload`, `tw.object.class` | Durable Object instances started in workerd |
| `tw.do.objects` | `tw_do_objects` | gauge | `tw.tenant.id`, `tw.workload`, `tw.object.class` (empty for workload nodes), `tw.object.status` | objects on this host by status (`ready`, `pending`, `killed`, …) |
| `tw.workloads` | `tw_workloads` | gauge | `tw.tenant.id`, `tw.workload.status` | workloads on this host by status |
| `tw.host.capacity` | `tw_host_capacity` | gauge | `tw.resource` (`nodes`, `vms`, `vm_memory_mib`, `huge_memory_mib`, `vcpus`, `scratch_mib`) | the host's capacity vector |
| `tw.host.usage` | `tw_host_usage` | gauge | `tw.resource` (the same, plus `containers`) | live usage against it |
| `tw.logs.dropped` | `tw_logs_dropped_total` | counter | `tw.tenant.id`, `tw.workload` | request records over the workload's rate (below) |
| `tw.public.rejected` | `tw_public_rejected_total` | counter | `tw.tenant.id`, `tw.workload`, `tw.public.reason` (`conns`, `ip_conns`, `rate_limited`, `too_large`) | public requests and connections the public listener's limits refused (added once a minute, with the `public_rejected` audit summary) |
| `tw.telemetry.export.dropped` | `tw_telemetry_export_dropped_total` | counter | `tw.tenant.id`, `tw.workload`, `tw.signal` | telemetry not exported (queue full, or the export failed) |
| `tw.telemetry.export.errors` | `tw_telemetry_export_errors_total` | counter | `tw.tenant.id`, `tw.workload`, `tw.signal` | failed OTLP exports |

### mgmt

| OpenTelemetry name | Prometheus name | Type | Attributes | What |
|---|---|---|---|---|
| `tw.mint.duration` (s) | `tw_mint_duration_seconds` | histogram | `tw.tenant.id`, `tw.mint.pooled` | an object's node mint, create to minted |
| `tw.mint.stage.duration` (s) | `tw_mint_stage_duration_seconds` | histogram | `tw.mint.stage` (`queued`, `load`, `claim`, `throttle`, `reclaim`, `broker`, `createkey`, `custody`, `commit`) | one stage of a mint attempt (the "mint spans" log line) |

Each metric keeps at most 2000 series; more fold into one series with the
attribute `otel_metric_overflow="true"`, so per-object attributes can't grow
a host's memory without bound.

CPU time isn't reported: workerd measures it only on Cloudflare's edge (its
tail events carry `cpuTime: 0`). Wall time is the request's duration at
hostd's ingress.

## Export to a collector: `tw telemetry set`

```sh
tw telemetry set --otlp http://otel-collector:4318 --header authorization
# Enter a secret value for header authorization: ********
tw telemetry          # show (header names only)
tw telemetry rm       # stop
```

Every workload of the tenant then exports, with OTLP/HTTP and the JSON
encoding, to `<endpoint>/v1/logs`, `/v1/metrics` and `/v1/traces`:

- **logs**: one log record per request (`event.name` `tw.request` or
  `tw.invocation`), one per `console.*` line and one per exception, all
  with the request's trace and span IDs;
- **metrics**: the workload's series of the hostd metrics above, every
  30 s (cumulative);
- **traces**: the spans below.

The resource is `service.name` = the workload, `service.namespace` = the
tenant ID, `service.instance.id` = `<host>/<workload>`, plus `host.name`,
`tw.tenant.id` and `tw.workload`.

**The collector is reached over the tailnet, from each workload's own
node.** It must be a tailnet peer that the policy lets the workload reach,
for example:

```jsonc
"grants": [
  {"src": ["tag:tw-wl-support"], "dst": ["tag:otel"], "ip": ["tcp:4318"]},
],
```

A collector on the public internet is refused, like any other egress. An
OpenTelemetry Collector with the default OTLP receiver works as is:

```yaml
receivers:
  otlp:
    protocols:
      http:
        endpoint: 0.0.0.0:4318
```

`--signals logs,traces` exports only some signals. Header values
(collector credentials) are stored encrypted in mgmt's custody like
[secrets](secrets.md); no command or API returns them, and they are never
logged or audited. `--header NAME=VALUE` is for values that aren't secret
(`x-scope-orgid=acme`). Hosts pick up a change within a minute.

## Traces

tailworks propagates the W3C `traceparent` header across every hop:

```
caller ──▶ hostd ingress ─────────▶ Worker ──stub.fetch()──▶ Durable Object ──fetch()──▶ hostd egress ──▶ next tailnet hop
           SERVER span                       CLIENT span "<Class> fetch"                  CLIENT span
           (parent: the caller's)            (parent: ingress)                            (parent: the DO's context)
```

- A tailnet caller's `traceparent` is honoured. A public (Funnel) request
  always starts a new trace.
- With no incoming `traceparent`, the trace ID is the request's audit trace
  ID, so `tw audit --trace <id>` finds the same request.
- Inside workerd, egress `fetch()` and a Durable Object stub's `fetch()`
  carry the running handler's context. If your code sets its own
  `traceparent` on a request, tailworks keeps it.
- Every span carries the object (`tw.object.id`, class and name), and the
  ingress span the verified caller and `tw.audit.trace_id`: each object is
  its own tailnet node, so a trace shows which object called which.
- Trace context is observability data. It never decides authorization,
  and the audit trail's trace IDs are always tailworks' own.

Invocations that have no request headers get their trace from the runtime
or from hostd:

- **RPC to a Durable Object** (`stub.method(...)`, through an `env`
  binding) gets a CLIENT span `<Class> <method>` in the caller and a
  SERVER span `<Class> <method>` in the object, its child. The runtime
  carries the context as one extra trailing argument, and the object's
  wrapper removes it before your method runs, so your method gets exactly
  the arguments the caller passed. The object's invocation record carries
  the SERVER span's trace and span IDs.
- **An alarm** starts a new trace: a root SERVER span `<Class> alarm`. The
  alarm's record carries it.
- **A cron run** starts a new trace too. hostd opens the root SERVER span
  `scheduled <workload>` (its trace ID is the run's audit trace ID), and
  `scheduled()` runs inside it, so the run's record has the same trace.
- Egress, `ctx.tailnet.fetch()`, stub `fetch()` and RPC made inside any of
  these join that trace, as they do inside a request. `ctx.tailnet.fetch()`
  uses the same propagating `fetch()`, so it always joins the caller's trace,
  and hostd's egress span is the hop's CLIENT span.
- workerd's tail event for an RPC call or an alarm has no headers, so it
  can't carry a `traceparent`. hostd matches the record to the runtime's
  SERVER span by object, by event (the alarm, or the RPC method) and by
  start time (within 2 s). Two calls to the same method of the same
  object that start within that window may get each other's trace IDs. A
  record whose span never arrives is written after the join wait, with no
  trace.

Not traced yet:

- RPC through a raw binding (one that skips tailworks' guarded `env`, such
  as `import { env } from "cloudflare:workers"`), RPC to a Worker's own
  entrypoints or a service binding, and RPC made while no handler's trace
  is running (for example from a WebSocket event). The callee runs with no
  trace.
- WebSocket events (`webSocketMessage`, `webSocketClose`), queue consumer
  batches, Workflow steps, and storage operations. Their records have no
  trace.

## Volume bounds

One noisy object can't fill a host's disk:

- a record keeps at most 100 `console.*` lines of 4 KiB each (16 KiB in
  all) and 8 exceptions; `truncated: true` marks a cut;
- each workload writes at most 200 records per second (bursts of 1000) to
  its request log. The rest are dropped and counted, and a
  `kind: "dropped"` record says how many;
- a request log rotates at 8 MiB, keeping one previous file. With
  `tw up --request-log-max-age 168h` (`tw-hostd -request-log-max-age`)
  the host also drops records older than that, every 10 minutes; off by
  default;
- `workerd.log` and the node runtime's `app.log` rotate at 16 MiB the same
  way, and `tw up`'s own `mgmt.log` and `hostd.log` at 32 MiB;
- the export queue is 4096 items per signal and workload; overflow is
  dropped and counted, never buffered to disk.

## Crash loops

A workerd that exits on its own is restarted by hostd. What happens next
depends on whether the deployment ever served:

- **It served** (its workerd answered its health check once): hostd
  restarts it forever, with exponential backoff: 200 ms, 400 ms, 800 ms, …
  doubling to a 5 min cap. The count starts over once a workerd has run
  for 30 s. From the second exit in a row the deployment's status is
  `crashloop`, and its error says how many times it exited, when the next
  restart is, and workerd's last log lines. `tw ls`, `tw ls --json`, the
  admin console and the mgmt API all show it. A crash-looping deployment
  keeps its namespace lease and its nodes; requests get `503
  upstream_unavailable` until a restart answers, and then it is `active`
  again on its own. No `tw deploy` is needed.
- **It never served** (a bad bundle or config, or workerd exits during the
  deploy): after 3 quick restarts the deployment is `failed`, within a few
  seconds, and hostd does not retry it. Fix it and deploy again.

Before each restart hostd SIGKILLs the old workerd's process group and
waits until it is empty, then removes the stale `ingress.sock`, so a
survivor can't keep the socket or the address (issue #43). The hostd log
has one `workerd exited; restarting` line per exit (`pid`, `attempt`,
`backoff`, the log tail) and `workerd recovered` when it serves again.
`tw logs <workload>` shows workerd's own output.
