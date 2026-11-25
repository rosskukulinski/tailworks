// A black-box test: `tw dev` runs the app (the same netd and workerd as a
// deployment, on a local control plane) and the test calls it as a tailnet
// identity from its own dev node. Needs tailworks on PATH (or TW_BIN).
// docs/testing.md has unit tests too (@tailworks/tailnet/testing, vitest).
import { test, before, after } from "node:test";
import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import { startDev } from "@tailworks/tailnet/testing/dev";

let app;
before(async () => {
  app = await startDev({
    dir: fileURLToPath(new URL("..", import.meta.url)),
    callers: { agent: { tags: ["tag:tw-agent-support"] } },
  });
});
after(() => app?.stop());

// callTool sends one MCP tools/call (streamable HTTP) and returns its result.
async function callTool(who, name, args = {}) {
  const r = await who.fetch("/mcp", {
    method: "POST",
    headers: { "content-type": "application/json", accept: "application/json, text/event-stream" },
    body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/call", params: { name, arguments: args } }),
  });
  const raw = await r.text();
  const data = raw.split("\n").find((l) => l.startsWith("data: "))?.slice(6) ?? raw;
  return JSON.parse(data).result;
}

test("tools see the verified caller; ungranted tools are refused", async () => {
  const agent = app.as("agent");
  const who = JSON.parse((await callTool(agent, "whoami")).content[0].text);
  assert.equal(who.nodeId, agent.nodeId);
  assert.deepEqual(who.tags, ["tag:tw-agent-support"]);

  // tw dev grants no example.com/cap/mcp, so lookup_order is refused
  const denied = await callTool(agent, "lookup_order", { order: "o1" });
  assert.equal(denied.isError, true);
  assert.equal(JSON.parse(denied.content[0].text).code, "cap_missing");
});
