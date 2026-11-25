Last update: 2026-11-24 PT. tw mcp on a terminal prints the registration help; --config covers every agent (#466, #467).

# simplify-mcp-config

Issues #466 and #467 from the interface-simplification audit (#434).

## What changed

- **#466** `tw mcp` without `--config` used to block silently on stdin when
  a person ran it. Now `mcpShowConfig` (cmd/tw/mcp.go) decides: `--config`
  or `--json` → the registration help; stdin a terminal (`mcpIsTerminal`,
  swappable like `confirmIsTerminal`) → the help plus one stderr line
  saying why and how to pipe into the bridge by hand; stdin a pipe → the
  bridge, unchanged. `--config` stays as the explicit form; `"mcp"` stays
  in `noUpdateNotice`. No `--stdio` flag: the pipe form
  (`printf … | tw mcp`) is documented instead (fewer flags). With `--json`
  no stderr note is printed (stdout is the result; stderr is for errors).
- **#467** `agent.MCPRegistrations` (agent/mcp.go) is one table of how to
  register the server with each agent `tw agent setup` knows: Claude Code
  (`claude mcp add -s user tw -- tw mcp`), Codex (`codex mcp add tw -- tw
  mcp`), Gemini CLI (`gemini mcp add -s user tw tw mcp`, server flags after
  `--`), Cursor (`~/.cursor/mcp.json`) and OpenCode
  (`~/.config/opencode/opencode.json`) JSON. Detected agents first.
  `tw mcp --config` prints it; its JSON keeps `url`, `stdio`, `tools`,
  `commands` (now one key per agent plus `json`) and adds `agents`.
  `tw agent setup` (after writing) and `tw agent ls` end with
  `next: register tailworks' MCP server with your agent: tw mcp --config`.
- Docs: docs/agents.md, docs/cli-json.md, README.md, agent/skills/tw/SKILL.md,
  schema/cli.schema.json (`mcp.agents`), docs/cli.md (`make cli-docs`),
  changes/simplify-mcp-config.md (cli-json, config-default).

## Tests

- `go test ./agent/` (TestMCPRegistrations): ok.
- `go test ./cmd/tw/ -run TestMCPShowConfig`: ok.
- `make lint`: ok.
- `make lint` after merging origin/master (7619df42): ok; `make cli-docs`: no change.
- `make test` after the merge: everything passes except three `hostd` tests
  that timed out on a heavily loaded machine (TestPythonQueues "workerd did
  not become healthy", TestWorkersKV/DeleteAndLimits client timeout,
  TestTailnetHTTPS 20m package timeout). This branch doesn't touch hostd;
  they were rerun on their own (see the PR). `cmd/tw`, `agent`, `mgmt`: ok.
- `TestAgentSetupSnapshot` golden updated for the new `next:` line.

Not run: `make demo-test` (no change to the mcp/agent templates' routes,
demo scripts, or `tw ls`/`tw status` JSON).
