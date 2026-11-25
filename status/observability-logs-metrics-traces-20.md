Last update: 2026-11-15 09:00 PT. Logs, metrics and traces (issue #20, ADR-observability): structured `twd logs`, Prometheus `/metrics`, OTLP export, W3C `traceparent` across DO calls and egress.

# Logs, metrics and traces

Design: [ADR-observability](../docs/design/adr/observability.md) (Proposed;
phases 2 and 3 are proposals). User docs:
[docs/observability.md](../docs/observability.md). Compatibility rows:
[docs/COMPAT.md](../docs/COMPAT.md) "Logs, metrics and traces".

## Built (phase 1)

- **`internal/obs`** (new, stdlib only): W3C traceparent; a metrics
  registry (counters, histograms, gauge funcs, 2000-series cap with an
  `otel.metric.overflow` series) with Prometheus text exposition and the
  OTel→Prometheus name translation; OTLP/HTTP JSON payloads (logs, metrics,
  traces) and a client; the request `Record` (filters, text rendering,
  per-record bounds, OTLP log records); `RotatingFile`, a token bucket,
  `TailRecords` and a rotation-aware `RecordFollower`. No new Go modules:
  the OTel SDK and its gRPC/protobuf stack were not added.
- **Capture**:
  - workerd `tails`: the tenant service gets `tails = [platform "Tail"]`
    (`hostd/gen/observe.go`); each facet's Dynamic Worker gets
    `tails: [ctx.exports.Tail({props: {kind: "object", class, name}})]`
    (attested). The platform has no tails (it would trace its own sink).
    The `Tail` entrypoint (`platform.js`) keeps only traceparent, path,
    method, status, route, logs (100 × 4 KiB), exceptions (8), outcome,
    and posts to hostd `POST /v1/tail`.
  - hostd ingress (`ingressHandler`, one call): SERVER span; a tailnet
    caller's traceparent is honoured, a public one never; root traces use
    the audit trace ID; workerd receives hostd's span as parent.
  - hostd egress (`serveEgress`, one call): CLIENT span per request, child
    of the runtime's context; the destination receives it as parent.
  - Runtime (`js/packages/tailnet/src/trace.ts`, new): AsyncLocalStorage
    (`nodejs_als`, added by gen unless `nodejs_compat`/`nodejs_als` is
    set) around every fetch handler (Worker, plain DO, facet); egress
    `fetch()` carries the context (a tenant-set traceparent wins); DO stub
    `fetch()` gets a child span, reported once per handler via
    `TailnetApi.call("spans")` → hostd `POST /v1/spans` (validated,
    bounded, attributed to the attested caller).
- **hostd pipeline** (`hostd/telemetry.go`, new): joins ingress and tail
  halves by span ID (2 s), writes `tenants/<t>/logs/<w>.requests.jsonl`
  (200 rec/s, burst 1000, 8 MiB rotation, `kind: dropped` notes), queues
  OTLP per workload (4096 per signal), exports logs/spans every 2 s and
  metrics every 30 s, from the workload's own node (`netd.DialEgress`).
  `workerd.log` and `app.log` are now `RotatingFile`s (16 MiB).
- **Metrics**: `http.server.request.duration`, `http.client.request.duration`
  (per object, `error.type`), `twd.invocations`, `twd.do.wakeups`,
  `twd.do.objects`, `twd.workloads`, `twd.host.capacity`, `twd.host.usage`,
  `twd.logs.dropped`, `twd.telemetry.export.{dropped,errors}`; mgmt:
  `twd.mint.duration`, `twd.mint.stage.duration` (from `mgmt/spans.go`).
  Served on port 9464 of each daemon's own tailnet node (`-metrics-port`;
  `twd up` serves host + mgmt on mgmt's node) and on hostd's local
  operator socket.
- **API** (`api/openapi.yaml`, `make api`):
  - `GET|PUT|DELETE /v1/tenants/{id}/telemetry` (admin; config in custody
    `twd/<tenant>/telemetry`; header values never returned/logged/audited;
    audited `telemetry_put`, `telemetry_delete`).
  - `POST /v1/tenants/{id}/workloads/{w}/telemetry-grants?epoch=N` (host;
    the lease holder at epoch only; audited `telemetry_release` once per
    host, workload and config version; refusals always).
  - `GET …/logs` gains `source=console|requests` and the filters `object`,
    `status`, `outcome`, `caller`, `trace`, `errors` (`application/x-ndjson`).
  - New `AuditKind`s: `telemetry_put`, `telemetry_delete`,
    `telemetry_release`. New schemas: `TelemetryConfig`, `TelemetryPut`,
    `TelemetryGrant`, `OTLPExport(Put)`, `OTLPGrant`, `RequestRecord`.
- **CLI**: `twd logs <workload>` now shows records (text; `--json`;
  `--object --status --outcome --errors --caller --trace`; `--console` for
  the raw output, locally and from a linked machine). `twd telemetry
  [show] | set --otlp URL [--header NAME[=VALUE]]… [--signals …] | rm`
  (a bare `--header NAME` reads the value from stdin, like `twd secret put`).

## Tests

All hermetic (testcontrol + local DERP + stock workerd 1.20261108.1),
macOS arm64:

- `go test -race ./internal/obs/`: traceparent parsing, Prometheus
  exposition (cumulative buckets, escaping, overflow, `DeleteSeries`), OTLP
  JSON shapes against a receiver (hex IDs, string int64s, enums), endpoint
  validation, filters/text/bounds, rotation + follow, limiter.
- `go test -race -run TestTelemetry ./hostd/` (4.4 s): one deploy of
  `hostd/testdata/apps/obsprobe` with a tsnet caller, echo and OTLP
  collector:
  - `TraceparentAcrossDOAndEgress`: caller `00-T-P` → Worker sees
    `00-T-S_in`, the facet DO `00-T-S_do`, the echo node `00-T-S_out`; four
    distinct spans, one trace.
  - `RecordsJoinIngressAndTail`, `PlainDOAndText`,
    `ExceptionAndNoQueryString` (no query string or ticket in the file),
    `LogsAPI` (filters on the host, the console still served),
    `PrometheusScrape`, `OTLPExportFromTheWorkloadNode` (every export's
    WhoIs is the workload node, with the tenant's header; spans form the
    tree SERVER(P) → CLIENT "Obj fetch" → CLIENT; no host-level series
    exported), `ExportStopsWhenUnset`.
- `go test -race -run 'TestRecordRateLimit|TestRecordLogRotates|TestJoin|TestIngressTraceContext|TestConsoleFollowAcrossRotation|TestExportQueueBound' ./hostd/`.
- `go test -run TestTelemetry ./mgmt/`: config CRUD and validation, values
  never in custody plaintext/logs/audit, the fenced grant, audit-once,
  roles (admins configure, hosts take grants, tenant code refused).
- `go test -run 'TestRemoteLogs|TestCLITelemetry' ./cmd/twd/`.
- `node --test js/packages/tailnet/test/trace.test.js`: propagation,
  tenant-set traceparent kept, concurrency, DO child spans, bounds.
- `make e2e-observability` (new, `-tags e2eobs`, 11 s): real `twd-mgmt` +
  `twd-hostd` processes. Measured: `TRACE 4bf92f35…: caller 00f067aa… →
  ingress 473e37f9… → DO call 5fa969e4… → egress 5df61d3a…`; records read
  through mgmt from the hostd process; `/metrics`; 4 OTLP exports (logs,
  metrics, traces) from `h1-obsprobe-…` with the custody credential;
  `telemetry_release` audited without it.
- After merging `origin/master` (platform stores, wrangler, README
  matrix): `make lint` passes (including `internal/compatdoc`), `make
  test-go` passes (`-race`; one `hostd` failure found by the full run,
  `TestLocalPushAPIKeepsOperatorRoutes`, fixed: `/metrics` on a bare
  `Host`), `make test-js` passes, and `make test-sdk` passes
  (`TestCloudflareAgents`, `TestAgentTemplate`, `TestVercelAI`: the
  runtime's new fetch wrappers under real SDK classes).

## Findings

- workerd's `TraceItem.cpuTime` and `wallTime` are always 0 off Cloudflare's
  edge: no CPU time metric. Wall time is hostd's ingress duration.
- `node:diagnostics_channel` (which reaches tails) needs full
  `nodejs_compat`; `AsyncLocalStorage` needs only `nodejs_als`. Hence span
  reports go over `TailnetApi`, not the tail.
- A Dynamic Worker's `WorkerCode.tails` accepts `ctx.exports.X({props})`:
  facet records are attested by the platform.
- A tail worker's own invocations are traced by its worker's tails
  (`event.consumedEvents`): the sink must be in a worker without tails.
- A DO invocation with no ingress half is written after `JoinWait` (2 s):
  `twd logs -f` shows internal DO calls ~2 s late.

## Gaps / next (phase 2 in the ADR)

- Not traced: DO RPC (`stub.method()`), alarms, cron runs.
- mgmt's own traces and OTLP export of twd's own telemetry.
- Per-workload overrides (`twd.jsonc`), sampling.
- The join table (20000) and the per-metric series cap (2000) are shared
  by all tenants of a host: a tenant with very many objects degrades
  others' joins (records still written, unjoined) and per-object egress
  series (folded into overflow), never loses them.
- The microVM console and node-tier output are not exported over OTLP.
