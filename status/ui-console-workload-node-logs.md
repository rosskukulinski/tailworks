# Admin UI: workload and node logs

Last update: 2026-11-23 16:10 PT. The admin UI shows logs for workloads and nodes (objects).

## What's built

- `/ui/tenants/{id}/workloads/{workload}/logs` (`mgmt/ui/logs.go`,
  `templates/logs.html`). It has two tabs:
  - **Requests**: ADR-045 request records, newest first, with each
    record's `console.*` lines and exceptions. Filters: node, status,
    caller, trace and errors, all applied on the host by the API.
  - **Console output**: the raw app output, oldest first, with ANSI
    escapes and control characters removed.
- `/ui/objects/{id}/logs`: the workload's records pinned to
  `object=<id>`. It works for killed objects through the 410 tombstone.
  `?object=` and `?source=console` can't widen it.
- Links from the object, deployment, tenant and overview pages.
- Both pages read the last 50/100/200/500 records or lines. The UI's
  existing 5 s `data-live` refresh keeps them up to date.
- Replicas: the `[host]` prefix is shown per line or record. A host's
  `tw: …` failure line is shown as a note above the list.
- `no_logs` and `unknown_workload` show an empty state. Other API
  errors are shown inline, and the page still renders.
- No API change. The UI uses the existing `GET /v1/…/logs`.
  `ui.call` is split into `call` + `fetch`, so the UI can read text and
  NDJSON.

## Tests

- `go test ./mgmt/ui/`: ok. New: `logs_test.go`. The logs pages were
  added to the shared page walk, so `TestUsesOnlyPublicAPI`,
  `TestHostileStringsEscaped` and the empty and populated render tests
  cover them.
- `go test ./mgmt/ -run UI`: ok. The logs pages were added to the
  spec-validated walk against a real mgmt.
- `make lint test`: see the PR.
- Visual check: rendered the populated pages and took screenshots of
  the Requests and Console tabs.

## Gaps

- The daemons' own logs (hostd, netd, mgmt, the `--component` logs) are
  still host-local. The API doesn't serve them remotely, so the UI can't
  show them either. Showing them needs a new operator-only endpoint over
  the mgmt → hostd channel.
- There's no streaming `follow`. The page refreshes every 5 s instead.
- No e2e against a live workerd. That path is covered by
  `e2e-observability`, which was not run here (no API change).
