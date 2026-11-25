---
kind: fix
upgrade: []
---
The `ai-agent` template now works when you follow its README. Its default
`vars.MCP_URL` was `http://mcp/mcp`, which named no node: a workload's node
is `tw-<workload>` (with `tw up`'s default `--hostname-prefix`), so the
agent got `peer_not_visible` even with the `mcp` template deployed. The
default is now `http://tw-mcp/mcp`, and the `node-workflow` README uses the
same address. The README walks through the whole two-workload setup:
deploying `mcp` under that name, the capability grant that gives the agent
its tools, what a working reply looks like, and that the scripted model
ignores the prompt. `tw init --template ai-agent` and the template listings
say it needs `mcp`. Apps already made from the template keep their old
`MCP_URL`: set it to `http://tw-mcp/mcp` (or `tw deploy --var MCP_URL=…`).
