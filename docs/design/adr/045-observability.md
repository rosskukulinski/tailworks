# ADR-045: Structured request logs, Prometheus metrics and OTLP traces, built from workerd tail events and tailworks' own hops

**Date:** 2026-11-15
**Status:** Accepted for phase 1 (2026-11-16, built and shipped in v0.2.0:
`tw logs`, `tw telemetry`, `/metrics`, [docs/observability.md](../../observability.md);
results in `status/observability-logs-metrics-traces-20.md`). Phases 2 and 3
("Phasing" below) are not built and remain proposals. Drafted as
`ADR-observability`.

**Context.**

- Before this, `tw logs <workload>` tailed one raw file: workerd's stdout
  (`workerd.log`), the node tier's `app.log` or a microVM's serial console.
  There were no per-request records, no metrics, no traces and no export.
  The only timings were diagnostic log lines (`mgmt/spans.go`, the "mint
  spans" line).
- Every request to a workload or a Durable Object crosses tailworks' own code
  twice: hostd's ingress handler (after netd's WhoIs) on the way in, and
  hostd's egress handler (before netd dials from the object's node) on the
  way out. Both already know the verified caller, the object, the workload
  and the tenant.
- Stock workerd supports **tail workers** (`Worker.tails` in
  `workerd.capnp`). Measured on the pinned workerd (1.20261108.1):
  - a tail receives one `TraceItem` per invocation, for the stateless
    Worker, for Durable Objects (`executionModel: "durableObject"`,
    `entrypoint` = the class) and for a Dynamic Worker loaded through the
    Worker Loader (`WorkerCode.tails`, with `props`), which is how tailworks'
    facet objects run;
  - it carries `logs` (every `console.*` call, with level and timestamp),
    `exceptions` (name, message, stack), `outcome`, and the request's URL,
    method, **all headers** and `cf`, and the response status;
  - `cpuTime` and `wallTime` are always 0 in workerd (they are only
    measured on Cloudflare's edge);
  - a tail worker's own invocations are traced by its worker's tails, so
    the tail sink must live in a worker that has none.
- `node:diagnostics_channel` (which also reaches the tail) needs the full
  `nodejs_compat` flag. `AsyncLocalStorage` needs only `nodejs_als`, which
  adds `node:async_hooks` and nothing else.
- The RFC (§4.10, SEC-15) says the platform generates each hop's trace ID
  and a guest-supplied one is only a `parentHint`; the audit trail relies on
  that (RT-02). W3C trace context is the opposite: the caller's
  `traceparent` decides the trace.
- [wdl](https://github.com/wdl-dev/wdl) (read for ideas, no code taken)
  ships per-invocation logs from a tail-like hook and Prometheus metrics,
  authenticated with bearer tokens. Tailworks' identity comes from the tailnet.

**Decision.**

1. **Two trace IDs, kept apart.**
   - The **audit trace ID** stays platform-generated per hop
     (`request.cf.traceId`, audit records, `getCaller().traceId`). Nothing a
     caller sends can set it.
   - The **W3C trace context** is observability data only. hostd's ingress
     accepts a tailnet caller's `traceparent` (a public, Funnel request
     always starts a new trace), opens a SERVER span, and hands workerd
     `traceparent: 00-<trace>-<that span>-01`. With no inbound
     `traceparent`, the trace ID is the hop's audit trace ID, so a
     single-hop request has one ID in both worlds. Every ingress span
     carries `tw.audit.trace_id`, so a trace links to `tw audit`.
     Trace context never feeds authorization or audit.
2. **Propagation inside workerd: the runtime, with `AsyncLocalStorage`.**
   - hostd adds `nodejs_als` to the tenant service's and the facets'
     compatibility flags (unless `nodejs_compat` or `nodejs_als` is already
     there).
   - The runtime (`@tailworks/tailnet` `runtime.ts`, `trace.ts`) runs each
     `fetch` handler (the Worker, plain-DO classes and facet classes) inside
     the request's trace context, and then:
     - **egress** `fetch()` gets the current context as `traceparent`
       (unless the tenant set a different one itself);
     - a **Durable Object stub's** `fetch()` gets a new child span. The
       runtime records it as a CLIENT span (`<Class> fetch`) and reports
       the invocation's spans to hostd once, at the end, through the
       platform's `TailnetApi` (`op: "spans"`; attested like every other
       op).
   - hostd's egress handler opens a CLIENT span per request (child of the
     runtime's context) and rewrites `traceparent` to it, so the next hop's
     ingress span (another tailworks object, or any OTel-instrumented server)
     joins the same trace: caller → workload → DO → egress → next object.
   - RPC calls to a DO stub (`stub.method()`) carry no headers and are not
     traced in phase 1 (phase 2 below).
3. **Logs: one structured record per invocation, from two halves.**
   - hostd's ingress produces the authoritative half: time, tenant,
     workload, deployment, object, class/name, **verified caller** (node,
     user, tags, or `public`), method, path (never the query string),
     status, duration, trace and span IDs, audit trace ID.
   - workerd's tail produces the other half: `console.*` lines,
     exceptions, outcome, entrypoint. The tenant service gets
     `tails = [platform "Tail"]` in `config.capnp`; each facet's Dynamic
     Worker gets `tails: [ctx.exports.Tail({props: {class, name}})]`, so a
     facet's records are attested by the platform. The platform worker has
     no tails (it would trace its own tail sink). The platform's `Tail`
     entrypoint keeps only what a record needs (it drops every request
     header but `traceparent`, the query string, `cf` except the route,
     and the create ticket) and posts the events to hostd's host API.
   - hostd joins the halves by span ID (the tail event's `traceparent` is
     the one hostd's ingress set), waiting at most 2 s for the other half.
     An invocation with no ingress half (a DO called from the Worker, an
     alarm, a cron run) is its own record (`kind: "invocation"`).
   - Records go to a per-workload JSON Lines file on the host,
     `<data>/tenants/<tenant>/logs/<workload>.requests.jsonl`, and, if the
     tenant configured OTLP, to the collector.
4. **`tw logs` shows the records.**
   - Plain text by default: one line per request (time, method, path,
     status, duration, object, caller, trace), the `console.*` lines and
     exceptions indented under it. `--json` prints the records as they
     are. Filters: `--object`, `--status` (`500`, `5xx`), `--outcome`,
     `--errors`, `--caller`, `--trace`. Filtering happens on the host, so
     `-n 50 --errors` is the last 50 errors.
   - `--console` shows the raw app output as before (`workerd.log`, the
     node tier's `app.log`, the microVM console).
   - Locally `tw logs` still reads the file directly; from a linked
     machine it goes through mgmt (`GET …/logs?source=requests&…`).
5. **Metrics: Prometheus on each daemon's tailnet node, OTLP per tenant.**
   - Names follow the OpenTelemetry semantic conventions
     (`http.server.request.duration`, `http.client.request.duration`, …)
     plus `tw.*` for what has no convention (invocations by outcome, DO
     objects by status, DO wake-ups, host capacity, dropped records, mint
     stage durations). The Prometheus names are the standard
     OTel→Prometheus translation (`http_server_request_duration_seconds`).
     The list is in `docs/observability.md`.
   - Each series set is bounded (2000 series per metric; more fold into an
     `otel_metric_overflow="true"` series), so per-object labels can't grow
     a host's memory without bound.
   - hostd and mgmt serve `GET /metrics` on their **own tailnet node**,
     port 9464 (`-metrics-port`, 0 = off), and nowhere else. Who can
     scrape is a grant in the tailnet policy to `tcp:9464`; there is no
     token. A single-process `tw up` serves both on mgmt's node.
   - These are operator metrics: they cover every tenant on the host. A
     tenant gets its own workloads' metrics over OTLP.
   - CPU time is not available: workerd reports 0. Wall time is hostd's
     ingress duration.
6. **OTLP export: per tenant, configured in custody, dialed from the
   workload's node.**
   - `PUT /v1/tenants/{id}/telemetry` (`tw telemetry set`) stores
     `{otlp: {endpoint, headers, signals}}` in mgmt's custody
     (`tw/<tenant>/telemetry`), like a secret (ADR-034): header values
     (collector credentials) are never logged, audited or returned; `GET`
     shows the endpoint and header names only.
   - `POST /v1/tenants/{id}/workloads/{w}/telemetry-grants?epoch=N` releases
     the config to the host holding the workload's lease at `epoch`, the
     same fence as secrets. hostd asks when the workload starts and every
     60 s.
   - hostd exports each workload's logs, spans and metrics with OTLP/HTTP
     (JSON encoding) **from the workload's own tailnet node** (netd's
     egress dialer, ADR-019 rules). The collector is a tailnet peer; the
     tailnet policy decides whether that workload may reach it. A public
     internet collector is refused like any other egress.
   - tailworks implements OTLP/HTTP+JSON and the Prometheus text format itself
     (about 700 lines, `internal/obs/otlp.go` and `metrics.go`), instead of
     adding the OpenTelemetry Go SDK, its exporters and their
     gRPC/protobuf dependencies.
   - With no config nothing is exported, and records and `/metrics` work as
     above: zero-config `tw up` has structured `tw logs` and metrics.
7. **Safety.**
   - Never logged by default: request and response bodies, query strings,
     request headers (only `traceparent` is read), `cf` (beyond the
     route), create tickets, secret values, OTLP header values.
   - Volume is bounded per workload, so a noisy object can't fill a disk:
     - a record keeps at most 100 `console.*` lines of 4 KiB each, 16 KiB
       in total, and 8 exceptions (`truncated: true` says so);
     - each workload may write 200 records/s (burst 1000) to its file;
       beyond that records are dropped and counted
       (`tw_logs_dropped_total`), and one `kind: "dropped"` record per
       second says how many;
     - each file rotates at 8 MiB (one previous file is kept);
     - `workerd.log` and the node tier's `app.log` are now size-capped the
       same way (16 MiB, one previous file);
     - the export queue is bounded per workload; overflow is dropped and
       counted, never buffered to disk.

**Phasing.**

- **Phase 1 (this branch):** everything above.
- **Phase 2:**
  - trace context over DO RPC (`stub.method()`), alarms and cron runs (a
    span per run, hostd-started). Done (#295): the runtime passes RPC
    context as a trailing marker argument that the callee's wrapper
    strips, opens a root span per alarm, and hostd opens one per cron
    run; RPC and alarm records are matched to their spans by object,
    event and start time (`docs/observability.md`);
  - `ctx.tailnet.fetch` spans in the runtime (not only hostd's). Not
    done: it joins the caller's trace (#295), and hostd's egress span
    already is the hop's CLIENT span, with `error.type` for refusals;
  - mgmt's own traces (the mint stages as spans, linked from hostd's
    credential wait) and OTLP export of tailworks' own telemetry
    (`OTEL_EXPORTER_OTLP_ENDPOINT` on the daemons);
  - per-workload overrides in `tailworks.jsonc` (`observability.otlp`, sampling);
  - egress records in `tw logs` (today egress is spans and metrics only;
    refusals are already audited).
- **Phase 3:** the microVM console and node-tier app output as OTLP logs;
  CPU time if workerd ever reports it; head sampling and tail-based
  retention.

**Consequences.**

- Every request now costs one extra local POST from workerd to hostd (the
  tail event) and, when the code calls a DO by stub fetch, one more (its
  spans). Both are unix-socket requests on the host, off the response path.
- `tw logs` changes its default from raw output to records. Scripts that
  read `tw logs <workload>` for app output need `--console`.
- `nodejs_als` is always on for tenant code. It only adds
  `node:async_hooks`.
- A tenant that sets its own `traceparent` on an egress `fetch()` keeps it;
  tailworks then only adds its egress span under it.
- A caller can choose the W3C trace ID of its requests (that is the point
  of propagation). That can mislead a tenant's trace view, never its
  audit trail, whose IDs it can't choose.
- The join table and each metric's series cap are per host, shared by all
  of its tenants. A tenant with very many objects can push others' records
  to be written unjoined and their per-object egress series into the
  overflow series; nothing is lost, and the bounds keep memory flat.
