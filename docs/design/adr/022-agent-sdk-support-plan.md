# ADR-022: Agent SDK support plan; MCP is stateless

**Date:** 2026-11-07
**Status:** Accepted (lead). Source: research/agent-sdks.md. Verified by the lead:
- `McpAgent` "is deprecated and feature-frozen" (developers.cloudflare.com/agents/api-reference/mcp-agent-api/).
- MCP 2026-07-28 removes protocol-level sessions and `Mcp-Session-Id` (modelcontextprotocol.io/specification/2026-07-28/changelog).
**Decision:**
- **MVP:**
  - The Cloudflare Agents SDK `Agent` runs natively. Its core (state, `this.sql`, scheduling, hibernating WebSockets) runs in workerd, but it needs a recent workerd, plus facets and `ctx.exports`.
  - MCP servers use the stateless `createMcpHandler`, wrapped by `@tailworks/mcp` `withTailnet`, which resolves the caller **per request**. This matches ADR-010: no session binding.
  - The Vercel AI SDK (`ToolLoopAgent`, `mcp-handler`) runs in-isolate. Estimated S, 1–1.5 eng-weeks.
- **v1:**
  - A Workflows engine for `AgentWorkflow`.
  - An **opt-in Node process tier** inside the microVM, for SDKs that need `node:vm`, child processes or Python. Estimated L, 6–10 eng-weeks. It measured ~100 MB and ~90 ms per agent, so it gets **one identity per process**, not per object.
  - Vercel Workflow / `WorkflowAgent` on that tier.
- **Client-only:** Kong Volcano (a hosted, closed platform launched 2026-09-30; its 2025 Agent SDK was archived 2026-10-08), Vercel's hosted services, Claude Agent SDK, LangGraph and OpenAI Agents. These reach tailworks tools over the tailnet.
**Effort:** Cloudflare Agents SDK full support is L, 5–9 eng-weeks. Supporting both actor-native and loop/hosted SDKs is ~7–12 eng-weeks, or 13–22 with the Node tier. All figures are estimates from research/agent-sdks.md.
**Also adopt:** MCP's `_meta` trace-context conventions (`traceparent`) for the SEC-15 trace ID; Client ID Metadata Documents rather than DCR for any OAuth path.
