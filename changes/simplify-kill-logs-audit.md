---
kind: breaking
upgrade: [cli-json, config-default]
---
Three day-2 commands now do the useful thing with no flags.

- `tw kill <object>` kills the object and its sub-agents (it cascades by
  default). `--cascade` is still accepted; `--cascade=false` kills only the
  object, and the reaper kills its sub-agents within seconds, as before.
  The cascade is now recorded in the audit trail as your kill (with your
  `--reason`), not the reaper's. `tw kill` still does not ask for
  confirmation.
- `tw logs` with no workload shows your app: the app in the current
  directory, else the tenant's only deployed workload (previews and
  replicas count as their workload). With several workloads it fails
  `bad_request` and lists one `tw logs <workload>` per workload. It no
  longer prints `hostd.log`, and it now works the same way from a linked
  machine. With `--json`, `logs.line` events carry `component` only for
  `--component`.
- `tw audit` with no object or mode shows the tenant's newest 50 records
  (`-n` for more), oldest first, still verified. When older records exist
  it says so and points at `tw audit --all`; with `--json` the result has
  `"truncated": true`. An object, `--human`, `--trace` and `--all` still
  list every record.

Operator action: scripts that read the host daemon's log with a bare
`tw logs` must pass `--component hostd`.
