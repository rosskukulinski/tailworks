Last update: 2026-11-24 PT. tw init: four starting points, `tw init --list`, one next-step hint (#439, #448).

## What's built

- `cmd/tw/templates.go`: the template table (name, purpose, needs, guide,
  starting point), `tw init --list` (before the arity check; `--json`
  honoured, schema `$defs/"init list"`), the help footer and the
  two-line next-step hint `printInitNext`.
- Help, usage and footers list `hello (default) | agent | mcp |
  static-site` plus `tw init --list`; `loadApp`'s fix string no longer
  enumerates templates. `docs/cli.md` regenerated.
- create-tailworks's no-`tw` fallback prints the same two lines with
  `npx tailworks`.
- README.md "Next", docs/README.md templates bullet, docs/INSTALL.md
  `tw link` comment.

## Decisions

- The `python` template (added after the audit, which counted eight) is
  in `--list`'s second section, titled "Demos, patterns and other
  runtimes"; the four starting points are the ones the issue names.
- `ai-agent` has no guide in docs/ (docs/demo.md uses `mcp` + `agent`), so
  it links its own README.
- The agent skill table and the create-tailworks README/package.json keep
  the full list, as the issue asks.

## Tests

See the PR's Tests section.

- `make lint`: ok.
- `make test`: cmd/tw, schema, docs and the rest pass; failures only in
  packages this branch doesn't touch (hostd, mgmt, internal/mgmtd), all
  timing budgets/timeouts on a machine at load average ~138 (parallel
  agents). `TestCLIEndToEnd` failed once there too and passed on rerun:
  `go test ./cmd/tw -run 'TestCLIEndToEnd|TestCLIJSONContract|TestInit|TestHelp|TestCLIReference'` ok.
- `make changes-check`: ok with `changes/init-starting-points.md`.
- create-tailworks fallback checked by running the bin without `tw` on PATH.
