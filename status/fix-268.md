Last update: 2026-11-23 PT. Issue #268: ai-agent template README and default MCP_URL.

# ai-agent template: make it work from its README (#268)

## What changed

- `templates/ai-agent/wrangler.jsonc`: default `MCP_URL` `http://mcp/mcp` →
  `http://tw-mcp/mcp`. Finding: netd resolves a bare name against the first
  DNS label of a peer (`netd/egress.go`), and a workload node's hostname is
  `<prefix><workload>` (`hostd/objects.go` `hostnameFor`), with `tw up`'s
  `--hostname-prefix` defaulting to `tw-`. So the old default never matched
  the `mcp` workload's node `tw-mcp`. The e2e suites never caught it: they
  pass `MCP_URL` with the server's IP.
- `templates/ai-agent/README.md`: end-to-end walkthrough (deploy `mcp` under
  that name, scaffold, the `example.com/cap/mcp` grant, deploy, curl),
  expected output (taken from `TestVercelAI/ToolLoopAgent/createMcpHandler`),
  the scripted model's fixed plan, troubleshooting, and the `tw dev` limit.
- `tailworks.jsonc` grant comment now includes the workload tag (for
  `/worker`); `tw init` renames both tags in it.
- `model.ts`, `test/app.test.js` comments; `node-workflow/README.md` URL.
- `tw init --template ai-agent` prints a line about the `mcp` dependency.
- Template listings (README.md, docs/README.md, docs/INSTALL.md,
  create-tailworks/README.md, tw-scaffold skill) note the dependency.

## Not done (option B in the issue)

A self-contained local demo needs `tw dev` to run two apps (or an embedded
policy evaluator granting `example.com/cap/mcp`); `tw dev` runs one app and
grants only the test cap today (`cmd/tw/dev.go` header). The template test
still asserts `peer_not_visible`.

## Tests

All on 2026-11-23, macOS, on top of master cfb66834; `make lint test` rerun and passing after merging master 85c4d89b:

- `make lint test`: pass.
- `make templates-typecheck`: pass (every template: npm install, tsc, tw types --check).
- `make test-sdk`: pass (includes `TestVercelAI`: ai-agent + mcp, the source of the README's expected output).
- `make changes-check`: ok (no upgrade-relevant paths).
- `tw init /tmp/…/ai-agent --template ai-agent`: prints the new hint line; the grant comment has the renamed tags, the same ones the README uses.
- Not run: a real-tailnet deploy of mcp + ai-agent (`e2e-real` needs credentials), so `http://tw-mcp/mcp` is checked against the code, not a live tailnet.
