---
kind: feature
upgrade: [cli-json, config-default]
---
`tw mcp` on a terminal now prints how to register tailworks' MCP server
(the same as `tw mcp --config`) instead of waiting silently for JSON-RPC
on stdin; agents, which run it with stdin a pipe, still get the bridge.
`tw mcp --json` means `tw mcp --config --json`. `tw mcp --config` now
prints the command or config for every agent `tw agent setup` supports
(Claude Code, Codex, Gemini CLI, Cursor, OpenCode), the ones installed
first, and its JSON gains an `agents` list and more `commands` keys (the
Claude Code command is now `claude mcp add -s user tw -- tw mcp`).
`tw agent setup` and `tw agent ls` end with a pointer to it.
