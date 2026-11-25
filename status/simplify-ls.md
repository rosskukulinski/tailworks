Last update: 2026-11-24 PT. Issues #452 and #459: `tw ls` lists every live object and says when it was cut short; `tw ls <workload>` filters by workload and labels previews.

# tw ls: live objects, truncation, workload filter (issues #452, #459)

Branch `simplify-ls`. Part of the simplify audit (#434).

## Built

- **#452.** `cmdLs` moved to `cmd/tw/ls.go` (out of `ops.go`).
  - The default asks mgmt for `state=pending|minted|assigned|ready`
    (`mgmt.LiveStatuses`), so killed tombstones no longer use up the
    listing. Stopped deployments are hidden unless `--all` is given.
  - `--all` adds killed objects and stopped deployments, and that is all
    it does. It no longer lifts the cap.
  - The cap is 5000 objects (`--limit N`, `0` = no cap). `--limit` is
    still parsed and listed under flags, but it is no longer in the usage
    line. No advanced flag group (B1) exists yet to move it into.
  - `truncated` (boolean, required in `$defs/ls`) is in `--json`. The
    human output prints `… and N more live objects (tw ls --limit 0 lists
    them all)`, with N taken from `nodes.tenant.total`. With `--all` or a
    workload filter the count isn't known, so it prints `… and more
    objects`.
  - `listUpTo` (v1client.go) returns the "more" bit, and `listAll` wraps
    it.
  - When an object's parent is not in the listing, the PARENT column
    shows the parent's ID.
  - The timeout is 30 s (it was 15 s).
  - MCP `ls` (mgmt/mcp.go) passes the same state filter unless `all` is
    set.
- **#459.** `tw ls [workload]` takes at most one workload. It keeps the
  deployments whose `Workload == w` or whose `_workload` namespace has
  `previewOf`/`replicaOf == w`. It fetches objects from mgmt with
  `workload=` for each selected name, so the cap applies to the
  selection.
  - Each preview gets the line `preview of <w> (alias <a>) · tw promote
    <w> <a>`. No new JSON fields.
  - A name with no deployment (stopped ones count) and no objects is
    `unknown_workload`.
  - `tw previews` is unchanged.

## Decisions

- The truncation hint says `tw ls --limit 0` instead of the issue's
  `(tw ls --all / --json)`. In the issue's own design, `--all` and
  `--json` stop at the same cap, so that hint would not show the missing
  objects.
- The node count (`nodes`) is still fetched on its own (tenant-wide,
  uncapped) and not shared with the listing fetch. With a workload filter
  or a cap the two are different sets.

## Tests

- `go test ./cmd/tw -run 'TestCLILs|TestCLINodeBudget|TestCLIJSON|TestSkill'`:
  ok. The new `TestCLILs` covers the default, `--all`, `--limit`, the
  workload filter, the preview line, the parent ID, `unknown_workload`
  and too many arguments.
- `go test ./mgmt -run TestMCP`: ok. MCP `ls` now leaves out killed
  objects by default.
- `make lint`: ok.
- `make test`: ok.
- `make demo-test`: SKIPPED by the script itself (no docker daemon on
  this machine).
- `make test-pg`: not run. No store or migration change (mgmt/mcp.go
  only).
