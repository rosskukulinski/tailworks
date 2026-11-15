# Logs, metrics and traces

twd records every request to your workloads and Durable Objects, keeps
metrics for them and for itself, and traces requests across tailnet hops.
It is built on open formats: Prometheus for scraping, and OpenTelemetry
(OTLP) for exporting logs, metrics and traces to a collector you run.

Nothing needs configuring for the first two:

- `twd logs <workload>` shows one structured line per request.
- Every host and mgmt serve `GET /metrics` on their own tailnet node.

Export to a collector is one command: `twd telemetry set`. The design is
[ADR-observability](design/adr/observability.md).

## Request logs: `twd logs`

```console
$ twd logs support
2026-11-15T10:00:00.123Z GET /agents/support-agent/alice 200 14ms  SupportAgent/alice  caller=ross@example.com  trace=4bf92f3577b34da6a3ce929d0e0e4736
    log   loaded 3 messages
2026-11-15T10:00:02.511Z POST /hook 500 3.2ms exception  caller=public:203.0.113.9  trace=9a1c…
    error TypeError: Cannot read properties of undefined (reading 'id')
          at Object.fetch (src/index.ts:41:18)
2026-11-15T10:00:02.530Z fetch GET /alice ok  SupportAgent  trace=4bf92f35…
    warn  cache miss
```

Each record is one request through twd's ingress (`kind: request`), or one
invocation that had none (`kind: invocation`: a Durable Object called from
your Worker, an alarm, a cron run, an RPC call). A record holds:

- the time, tenant, workload and deployment;
- the object (twd object ID, class and name);
- the **verified caller**: the tailnet node, the user for a person's
  device, its tags, or `public` and the client IP for a Funnel request;
- the method, path, status and duration;
- the invocation's outcome (`ok`, `exception`, `canceled`, …);
- its `console.*` lines (with their level) and uncaught exceptions;
- the W3C trace and span IDs, and the audit trace ID (`twd audit --trace`).

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

The filters run on the host, so `twd logs api --errors -n 20` is the last 20
errors, not the errors among the last 20 records. From a `twd link`ed
machine `twd logs` works the same (mgmt reads the host holding the
workload's lease).

A record never contains request or response bodies, query strings, request
headers, secret values or create tickets.

A request and its workerd invocation are reported separately and joined on
the host; a record is written when both halves are in, or 2 s after the
first. A Durable Object invocation with no ingress half (called from your
Worker) appears about 2 s after it ran.

The records are in `~/.twd/hostd/tenants/<tenant>/logs/<workload>.requests.jsonl`
on the host.

## Metrics: `GET /metrics`

hostd and mgmt serve the Prometheus text format on **port 9464 of their own
tailnet node**, and nowhere else. There is no token: the tailnet policy
decides who can scrape. Grant your Prometheus (here tagged `tag:prometheus`)
the port:

```jsonc
"grants": [
  {"src": ["tag:prometheus"], "dst": ["tag:twd-host", "tag:twd-mgmt"], "ip": ["tcp:9464"]},
],
```

Then scrape the nodes by MagicDNS name:

```yaml
scrape_configs:
  - job_name: twd
    static_configs:
      - targets: ["twd-mgmt.<tailnet>.ts.net:9464", "<host-id>.<tailnet>.ts.net:9464"]
```

`twd up` runs mgmt and the host in one process with one node, `twd-mgmt`,
which serves both sets of metrics. `-metrics-port` on `twd-hostd` and
`twd-mgmt` changes the port (`0` turns it off).

These are operator metrics: they cover every tenant on the host. A tenant
gets its own workloads' metrics through its OTLP export (below).

Names follow the OpenTelemetry semantic conventions where one exists, and
the standard OpenTelemetry → Prometheus translation (dots become `_`, the
unit becomes a suffix, counters end in `_total`).

### hostd

| OpenTelemetry name | Prometheus name | Type | Attributes | What |
|---|---|---|---|---|
| `http.server.request.duration` (s) | `http_server_request_duration_seconds` | histogram | `http.request.method`, `http.response.status_code`, `twd.tenant.id`, `twd.workload`, `twd.object.class` | every request to a workload or object, any tier, through hostd's ingress (requests, errors and latency) |
| `http.client.request.duration` (s) | `http_client_request_duration_seconds` | histogram | `http.request.method`, `http.response.status_code`, `error.type` (a refusal: `peer_not_visible`, `forbidden_destination`, …), `twd.tenant.id`, `twd.workload`, `twd.object.id` | egress, **per object** |
| `twd.invocations` | `twd_invocations_total` | counter | `twd.tenant.id`, `twd.workload`, `twd.object.class`, `twd.invocation.event` (`fetch`, `alarm`, `rpc`, `scheduled`, …), `twd.invocation.outcome` | workerd invocations (from its tail) |
| `twd.do.wakeups` | `twd_do_wakeups_total` | counter | `twd.tenant.id`, `twd.workload`, `twd.object.class` | Durable Object instances started in workerd |
| `twd.do.objects` | `twd_do_objects` | gauge | `twd.tenant.id`, `twd.workload`, `twd.object.class` (empty for workload nodes), `twd.object.status` | objects on this host by status (`ready`, `pending`, `killed`, …) |
| `twd.workloads` | `twd_workloads` | gauge | `twd.tenant.id`, `twd.workload.status` | workloads on this host by status |
| `twd.host.capacity` | `twd_host_capacity` | gauge | `twd.resource` (`nodes`, `vms`, `vm_memory_mib`, `huge_memory_mib`, `vcpus`, `scratch_mib`) | the host's capacity vector |
| `twd.host.usage` | `twd_host_usage` | gauge | `twd.resource` (the same, plus `containers`) | live usage against it |
| `twd.logs.dropped` | `twd_logs_dropped_total` | counter | `twd.tenant.id`, `twd.workload` | request records over the workload's rate (below) |
| `twd.telemetry.export.dropped` | `twd_telemetry_export_dropped_total` | counter | `twd.tenant.id`, `twd.workload`, `twd.signal` | telemetry not exported (queue full, or the export failed) |
| `twd.telemetry.export.errors` | `twd_telemetry_export_errors_total` | counter | `twd.tenant.id`, `twd.workload`, `twd.signal` | failed OTLP exports |

### mgmt

| OpenTelemetry name | Prometheus name | Type | Attributes | What |
|---|---|---|---|---|
| `twd.mint.duration` (s) | `twd_mint_duration_seconds` | histogram | `twd.tenant.id`, `twd.mint.pooled` | an object's node mint, create to minted |
| `twd.mint.stage.duration` (s) | `twd_mint_stage_duration_seconds` | histogram | `twd.mint.stage` (`queued`, `load`, `claim`, `throttle`, `reclaim`, `broker`, `createkey`, `custody`, `commit`) | one stage of a mint attempt (the "mint spans" log line) |

Each metric keeps at most 2000 series; more fold into one series with the
attribute `otel_metric_overflow="true"`, so per-object attributes can't grow
a host's memory without bound.

CPU time isn't reported: workerd measures it only on Cloudflare's edge (its
tail events carry `cpuTime: 0`). Wall time is the request's duration at
hostd's ingress.

## Export to a collector: `twd telemetry set`

```sh
twd telemetry set --otlp http://otel-collector:4318 --header authorization
# Enter a secret value for header authorization: ********
twd telemetry          # show (header names only)
twd telemetry rm       # stop
```

Every workload of the tenant then exports, with OTLP/HTTP and the JSON
encoding, to `<endpoint>/v1/logs`, `/v1/metrics` and `/v1/traces`:

- **logs**: one log record per request (`event.name` `twd.request` or
  `twd.invocation`), one per `console.*` line and one per exception, all
  with the request's trace and span IDs;
- **metrics**: the workload's series of the hostd metrics above, every
  30 s (cumulative);
- **traces**: the spans below.

The resource is `service.name` = the workload, `service.namespace` = the
tenant ID, `service.instance.id` = `<host>/<workload>`, plus `host.name`,
`twd.tenant.id` and `twd.workload`.

**The collector is reached over the tailnet, from each workload's own
node.** It must be a tailnet peer that the policy lets the workload reach,
for example:

```jsonc
"grants": [
  {"src": ["tag:twd-wl-support"], "dst": ["tag:otel"], "ip": ["tcp:4318"]},
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

twd propagates the W3C `traceparent` header across every hop:

```
caller ──▶ hostd ingress ─────────▶ Worker ──stub.fetch()──▶ Durable Object ──fetch()──▶ hostd egress ──▶ next tailnet hop
           SERVER span                       CLIENT span "<Class> fetch"                  CLIENT span
           (parent: the caller's)            (parent: ingress)                            (parent: the DO's context)
```

- A tailnet caller's `traceparent` is honoured. A public (Funnel) request
  always starts a new trace.
- With no incoming `traceparent`, the trace ID is the request's audit trace
  ID, so `twd audit --trace <id>` finds the same request.
- Inside workerd, egress `fetch()` and a Durable Object stub's `fetch()`
  carry the running handler's context. If your code sets its own
  `traceparent` on a request, twd keeps it.
- Every span carries the object (`twd.object.id`, class and name), and the
  ingress span the verified caller and `twd.audit.trace_id`: each object is
  its own tailnet node, so a trace shows which object called which.
- Trace context is observability data. It never decides authorization,
  and the audit trail's trace IDs are always twd's own.
- Not traced yet: RPC calls to a Durable Object (`stub.method()`), alarms
  and cron runs (their records have no trace), and `ctx.tailnet.fetch`
  beyond hostd's egress span.

## Volume bounds

One noisy object can't fill a host's disk:

- a record keeps at most 100 `console.*` lines of 4 KiB each (16 KiB in
  all) and 8 exceptions; `truncated: true` marks a cut;
- each workload writes at most 200 records per second (bursts of 1000) to
  its request log. The rest are dropped and counted, and a
  `kind: "dropped"` record says how many;
- a request log rotates at 8 MiB, keeping one previous file;
- `workerd.log` and the node runtime's `app.log` rotate at 16 MiB the same
  way;
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
  restart is, and workerd's last log lines. `twd ls`, `twd ls --json`, the
  admin console and the mgmt API all show it. A crash-looping deployment
  keeps its namespace lease and its nodes; requests get `503
  upstream_unavailable` until a restart answers, and then it is `active`
  again on its own. No `twd deploy` is needed.
- **It never served** (a bad bundle or config, or workerd exits during the
  deploy): after 3 quick restarts the deployment is `failed`, within a few
  seconds, and hostd does not retry it. Fix it and deploy again.

Before each restart hostd SIGKILLs the old workerd's process group and
waits until it is empty, then removes the stale `ingress.sock`, so a
survivor can't keep the socket or the address (issue #43). The hostd log
has one `workerd exited; restarting` line per exit (`pid`, `attempt`,
`backoff`, the log tail) and `workerd recovered` when it serves again.
`twd logs <workload>` shows workerd's own output.
