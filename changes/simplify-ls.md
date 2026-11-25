---
kind: feature
upgrade: [cli-json, config-default]
---
`tw ls` lists every live object: it asks mgmt for live objects only, so
killed objects no longer use up the listing and hide the newest ones. It
lists up to 5000 objects. If there are more, it prints `… and N more live
objects` and sets the new `truncated` field in `--json`. `--all` now only
adds killed objects and stopped deployments. Stopped deployments are no
longer listed without `--all`. `--limit N` still sets the cap (`0` means
no cap). The PARENT column shows the parent's ID when the parent is not
listed.

`tw ls <workload>` lists one workload with its previews and replicas. Each
preview has a line under it: `preview of <workload> (alias <alias>) · tw
promote <workload> <alias>`. A name with no deployment and no objects is
`unknown_workload`.

The MCP `ls` tool also asks for live objects only, unless `all` is set.
