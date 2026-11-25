# ADR-035: tailworks' own MCP server on mgmt, gated per tool by a tailnet grant

**Date:** 2026-11-15
**Status:** Accepted (2026-11-16, built and shipped in v0.2.0: mgmt's `/mcp`,
`tw mcp`; results in `status/agent-first-dx-issue-25.md`).

**Context.**

- Issue #25 asks for an MCP server for tailworks itself, so a coding agent on any
  tailnet machine can deploy, list, read logs and audit, and kill, with no
  credentials on disk, authorized by tailnet identity like the rest of tailworks.
  It also asks that destructive tools sit behind a grant.
- mgmt already serves `/v1` (and `/ui`, `/v2`) on its tsnet listeners
  behind one WhoIs gate (`TailnetHandler`, ADR-026). Today a tailnet caller
  is either a host (`tag:tw-host`) or an admin (`github.com/rosskukulinski/tailworks/cap/admin`).
  Everyone else gets `403 not_admin`.
- An admin can already do everything over `/v1`. An agent that holds the
  admin capability can kill anything, whatever the MCP layer says. A
  per-tool gate only means something if an agent can be given *less* than
  admin.
- Agents run commands without a terminal, so prompts don't protect anything;
  the policy has to.

**Decision.**

1. **Mount `/mcp` next to `/v1`** on the same listeners, through the same
   gate. MCP Streamable HTTP, stateless: one JSON-RPC message per POST,
   answered with `application/json`; `GET` is `405`. There are no sessions,
   so any mgmt replica can answer any request.
2. **Tools run the `/v1` operations in process** (`mgmt/mcp.go`): `whoami`,
   `status`, `ls`, `logs`, `audit` (read), `deploy` (write: a given bundle,
   or a redeploy of the current one), `kill` and `undeploy` (destructive).
   Validation, error codes and audit records are the API's. The actor is
   the MCP caller.
3. **A new app capability, `github.com/rosskukulinski/tailworks/cap/mcp`,** on a grant to
   `tag:tw-mgmt`. Its values list tools: `[{"tools": ["kill"]}]`, or `"*"`.
   - Holding it, with any value, gives the read tools. A caller that holds
     it without the admin capability is a new principal kind, `mcp`. The
     gate admits that kind to `/mcp` only, never to `/v1`, `/ui` or `/v2`.
   - `deploy`, `kill` and `undeploy` need a value naming them, **even for
     an admin.** Being an admin over `/v1` doesn't make an agent's MCP
     session destructive.
   - A tool the caller lacks is a tool error `cap_missing` whose `snippet`
     is the grant that would add it.
4. **The local operator** (mgmt's own node or the unix socket) gets the
   read tools, or exactly the tools the client lists in the
   `Tw-MCP-Tools` header. The `tw mcp` stdio bridge sends the read tools
   plus `--allow`. For tailnet callers the header only narrows their set.
   On the tailworks host, an agent is read-only unless the operator chose
   otherwise when registering it.
5. **`tw mcp`,** a stdio MCP server in the CLI that relays to `/mcp` over
   the transport the CLI already uses (the loopback on the host, the
   user's own Tailscale from a `tw link`ed machine). Every agent speaks
   stdio, and no credentials are involved. `tw mcp --config` prints the
   registration commands and the grant for the tools you lack.
6. **Browsers are refused** at `/mcp` (`browserGuard`), and so are hosts.
   The route is documented in `api/openapi.yaml` under the `mcp` tag,
   excluded from code generation like the OCI registry.

**Consequences.**

- An operator can give an agent a narrow, revocable slice of tailworks from the
  policy file: read-only by default, with `kill` for one tagged machine.
  Each grant shows in the policy and each action in the audit trail with
  the caller's identity.
- `WhoIsInfo` and `Principal` now carry capability values (`CapValues`),
  not just names, for `github.com/rosskukulinski/tailworks/cap/mcp`.
- `deploy` over MCP takes a bundle. An agent can't build one without the
  CLI, so in practice it redeploys (or re-places, `replace: true`) what is
  there. New code still goes through `tw deploy`.
- Not done: no secrets, tenants, hosts, expose or telemetry tools; no
  server-to-client notifications; no per-tool limits (counts, budgets) of
  the kind `@tailworks/mcp` has for tenant MCP servers.
