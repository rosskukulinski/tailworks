Last update: 2026-11-14 PT. `twd logs <workload>` works from a machine linked with `twd link`.

# Remote workload logs (`twd logs` over `twd link`)

Before this, `twd logs` on a linked machine failed with `remote_unsupported`:
the app log is a file on the host (`hostd /v1/status` → `LogFile`).

## What's built

- **mgmt API** (`api/openapi.yaml`, generated code via `make api`):
  `GET /v1/tenants/{id}/workloads/{workload}/logs?n=&follow=&host=`,
  `x-twd-role: admin` (same gate as every other tenant-scoped read, e.g.
  `GET …/workloads/{workload}/public`). `text/plain` body: the last `n`
  lines (default 50, 0..10000, else `400 bad_request`), then with
  `follow=true` a stream of new lines. New error codes `no_logs` (404) and
  `host_unavailable` (502), inserted in sorted position in `ErrorCode`.
- **mgmt** (`mgmt/logs.go`): resolves the workload and each replica
  (`replicasOf`, `<workload>--r<n>`) to its namespace lease holder, then
  opens hostd's `GET /v1/logs` on the host's push target (tsnet node through
  `TailnetDial`, or the local-dev unix socket), with the same stale-target
  retry as `push`. One source is copied as is; several are prefixed
  `[<hostID>] ` per line (in replica order; interleaved by line with
  `follow`). A host that doesn't answer contributes one `[<host>] twd: …`
  line; when none answers, `502 host_unavailable`. No client timeout:
  the stream lives as long as the caller's request.
- **hostd** (`hostd/logs.go`, one route line in `hostd/http.go`'s push API):
  `GET /v1/logs?tenant=&workload=&n=&follow=1`. The file is resolved from
  hostd's own `workloads[tenant/workload]` record (the active instance's
  `workerd.log`, the node tier's `app.log`, or the microVM console), never
  from a caller path. Tenant ID (`tenantIDRE`, no `..`) and workload
  (DNS label) are validated first, so `a`+`b/c` can't alias `a/b`+`c`.
  Tail reads backwards in 64 KiB chunks, at most 16 MiB. Follow polls every
  250 ms, handles truncation, switches to the new instance's log after a
  redeploy (re-resolved every 2 s), ends when the workload goes away, and
  is capped at 1 h per stream.
- **CLI** (`cmd/twd/logs_remote.go`, `hostd/mgmtapi/logs.go`; `ops.go` gets
  one `--host` flag and one call): when `s.remote()`, `twd logs <workload>
  [-n N] [-f] [--host <id>] [--tenant …]` uses the endpoint through the
  generated client (no 60 s client timeout). `-f` re-follows with `n=0`
  when the host ends the stream (Ctrl-C stops). `--component …` and a bare
  `twd logs` stay `remote_unsupported` with a fix line. Local behavior is
  unchanged.
- **Audit:** none. The spec audits mutations only (`x-twd-audit` is on no
  `GET`), so a log read adds no `AuditKind`; mgmt logs
  `workload logs read` with tenant, workload, hosts, follow and the actor.
- `mgmt/api/v1/validate.go`: the test validator's recorder gets `Unwrap`,
  so `http.ResponseController` can flush a stream through it.
- `docs/INSTALL.md`: the link section and the troubleshooting "Logs" line.

## Tests

- `go test -race -count=1 -run 'TestWorkloadLogFile|TestLogsHandler|TestTailLines' ./hostd/`:
  resolution (other tenant both ways, key confusion, `..`, encoded and
  non-label names, no active instance, node tier), tail/default/0/over-cap,
  `no_logs`, long lines and the byte bound, follow incl. a redeploy switch.
- `go test -race -count=1 -run TestWorkloadLogs ./mgmt/`: through the spec
  validator; tenant and workload come from the path; other tenant /
  unknown tenant / unknown workload / `?host=` miss / `n` over the cap are
  refused before any host is called; host `no_logs` passes through; a
  dead host is `502 host_unavailable`; host, plain user and tagged
  non-admin are 403, no WhoIs 401, an admin by the cap 200; replicas are
  prefixed per host, `?host=` and a replica name pick one.
- `go test -race -count=1 -run TestRemoteLogs ./cmd/twd/`: a linked state
  dir against a real in-process mgmt behind `TailnetHandler` and a fake
  hostd: tail, `-f` until interrupted, and the refusals with their codes.
- `TestCLILink` (needs workerd + node) now also runs `twd logs <workload>`
  from the linked box; not run on this machine (no workerd).
- `make lint`: ok (Redocly `make api-lint` has the same 4 pre-existing `/v2` errors as master, none new). `make test`: ok (exit 0; TestCLILink skips without workerd).

## Gaps / next

- Lines written while the CLI reconnects after the 1 h follow cap are lost
  (it re-follows with `n=0`).
- hostd's push port is protected by the tailnet policy (only `tag:twd-mgmt`
  → `tag:twd-host` `tcp:7474`), as for kills; there is no WhoIs check on
  the push listener itself. The logs route is no wider than `/v1/status`
  there.
- No per-tenant (non-admin) log access: twd has no tenant-scoped user role
  yet; reads are admin-only like `twd ls`.
- Container (ADR-032) and daemon logs are not served remotely.
