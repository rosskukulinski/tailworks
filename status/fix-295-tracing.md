Last update: 2026-11-23 21:30 PT. #295 item 4: DO RPC, alarms and cron runs are traced.

# fix-295-tracing: tracing gaps (#295 item 4)

## What's built

- **DO RPC** (`stub.method(...)` through a guarded `env` binding). The
  caller's guarded stub opens a CLIENT span `<Class> <method>` and appends
  one marker argument, `{__tw_tp: "<traceparent>"}`
  (`js/packages/tailnet/src/rpctrace.ts`). The callee's entry gate strips
  it before tenant code runs and runs the method under a SERVER span, the
  CLIENT span's child. Plain DOs use the existing kill gate; facets now
  return a trace-only Proxy (`traceGate`) from their wrapper's
  constructor. The RPC call is unchanged when no trace is running.
- **Alarms**: a plain DO's `alarm()` and a facet's delegated `__tw_alarm`
  run under a root SERVER span `<Class> alarm`.
- **Cron runs**: hostd opens the root SERVER span `scheduled <workload>`
  (`hostd/crontrace.go`; trace ID = the run's audit trace ID) and sends it
  as `traceparent`. The platform passes it on to the tenant
  (`platform.js scheduled()`), and the runtime runs `scheduled()` inside
  it. The run's tail record carries it and is written at once (there is no
  ingress half to wait for).
- **Records**: workerd's tail event for RPC and alarms has no headers, so
  hostd matches the record to the runtime's SERVER span
  (`hostd/telemetry_entries.go`). The match is by workload, object
  (`tw.do.id` = the tail's `durableObjectId` for a plain DO, the attested
  name for a facet), event (`alarm` or `rpc:<method>`) and the nearest start
  time within 2 s. Whichever half arrives first waits up to JoinWait.
- **`ctx.tailnet.fetch`** already used the propagating `fetch()`. It now
  also joins inside RPC methods, alarms and cron runs. A unit test covers it.
  No separate runtime span was added: hostd's egress span is the hop's
  CLIENT span (ADR-045 phase 2 notes this).
- Docs: `docs/observability.md` (new section, and what's still not traced),
  `docs/COMPAT.md` tracing row, and ADR-045 phase 2 notes.

## Tests run

- `make lint`: passes (local), again after merging `origin/master` (d9519164), with `go test ./hostd/ -run 'TestTelemetry$|TestEntrySpans|Cron'`.
- `make changes-check`: ok (no upgrade-relevant paths). A `feature`
  fragment was added anyway, `changes/fix-295-tracing.md`.
- `node --test test/*.test.js` in `js/packages/tailnet`: 49 pass,
  including the new `test/rpctrace.test.js`.
- `go test ./hostd/ -run 'TestTelemetry$'` (local, stock workerd): passes,
  including the new `TestTelemetry/EntryTraces/{ObjRPC,ObjAlarm,PlainRPC,PlainAlarm,CronRun}`.
- `go test ./hostd/ -run TestEntrySpansJoinRecords`: passes.
- `go test ./hostd/ ./hostd/gen/ -run 'Cron|Scheduled|Entry|Kill|RPC'`: passes.
- mbp runner: `make test e2e-observability templates-typecheck test-sdk`: all pass (exit 0; `TestObservabilityHermetic`, `TestCloudflareAgents`, `TestAgentTemplate`, `TestVercelAI` included).

## Findings

- workerd's tail events for RPC (`rpcMethod`) and alarms
  (`scheduledTime`) carry no headers or arguments. That is why records are
  matched to spans by time, not by ID. Two calls to the same method of the
  same object that start within 2 s may swap trace IDs.
- `node:diagnostics_channel` reaches the tail but needs full
  `nodejs_compat` (ADR-045), so it can't carry the trace ID into the tail
  event. A `console.*` marker would also show up in workerd's stdout
  (`tw logs`), so it isn't used.
- `ctx.id.name` may be absent in a plain DO's alarm. The span then carries
  only `tw.do.id`, which is enough to match the record.

## Not traced (documented in docs/observability.md)

- RPC through a raw binding, to a Worker entrypoint or service binding, or
  made while no trace is running (for example from a WebSocket event).
- WebSocket events, queue batches, Workflow steps, storage operations.
