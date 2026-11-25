Last update: 2026-11-24 PT. Bare-command defaults for tw kill, tw logs and tw audit (#451, #453, #454; tracker #434).

## What's built

- **#451 `tw kill` cascades by default.** `--cascade` defaults true and the
  CLI always sends `?cascade=<value>`; `--cascade=false` opts out (the
  reaper kills the orphans within `ReapInterval`). `--cascade` is still
  accepted. The MCP `kill` tool's `cascade` description says the CLI
  cascades by default; the tool and the admin UI checkbox keep their
  false default (a follow-up, as the issue says). `tw kill` still does not
  ask for confirmation.
- **#453 bare `tw logs` shows your app.** `cmd/tw/logs_default.go`: the app
  in the current directory (`loadApp(".")`), else the tenant's only
  workload (host: hostd's workloads of the tenant; linked machine:
  `/v1/deployments?latest=true` minus stopped; previews and replicas count
  as their base), else `bad_request` with one `tw logs <workload>` per
  workload plus the `--component` line. Never hostd.log. `logs.line`
  JSON no longer gets a `component: hostd` label it didn't earn. Host or
  mgmt unreachable: the error's fix still names `--component`. On a linked
  machine the fix says `--component` works on the host (it is still
  `remote_unsupported` from a linked machine).
- **#454 bare `tw audit`** = `GET /v1/audit?order=desc&limit=<n>` (`-n`,
  default 50, pages of at most 500 followed in desc order), shown oldest
  first; `older records: tw audit --all (verifies every chain)` and JSON
  `truncated: true` when a cursor remains. An object, `--human`, `--trace`
  and `--all` stay complete. Verification footer and `audit_unverified`
  unchanged. `schema/cli.schema.json` gains `truncated`.

Docs: docs/concepts.md, alternatives.md, INSTALL.md, RFC.md (one
sentence), errors.md (`remote_unsupported`), limits.md (`tw audit -n`
row), cli-json.md, the tw-teardown and tw-debug skills, the agent
template README, `make cli-docs`. Fragment
`changes/simplify-kill-logs-audit.md` (`breaking`, `[cli-json,
config-default]`, operator note on `--component hostd`).

Tests: `cmd/tw/simplify_defaults_test.go` (kill default / `--cascade` /
`--cascade=false` against a fake linked mgmt; bare audit `-n`, ordering,
`truncated`, `verified`, `--all` complete, `-n 0`; bare logs remote: one
workload with replica + preview, two workloads → bad_request, app dir
wins; bare logs on the host reads the workload, not hostd.log;
`baseWorkloads`). `TestRemoteLogs`' bare case now expects `bad_request`.

## Commands run

- `make lint`: ok.
- `make test`: all packages ok except `internal/mgmtd`
  `TestStartCloseBudget` (13.5 s, a timing budget, on a machine loaded by
  other agents' runs); rerun alone `go test ./internal/mgmtd -run
  TestStartCloseBudget -count=1`: ok.
- `make e2e-observability`: ok (TestObservabilityHermetic).
- `make demo-test`: SKIPPED by the script (no docker daemon here).
