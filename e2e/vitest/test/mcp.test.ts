// The mcp template (createMcpHandler + @tailworks/mcp) under @cloudflare/vitest-plugin:
// per-caller authorization from the caller's app capability, which
// withCaller sets. docs/testing.md.
import { exports } from "cloudflare:workers";
import { describe, it, expect } from "vitest";
import { createTestTailnet } from "@tailworks/tailnet/testing";

const tw = createTestTailnet();
const CAP = "example.com/cap/mcp";

async function callTool(who: object, name: string, args: object = {}) {
  const req = new Request("http://mcp/mcp", {
    method: "POST",
    headers: { "content-type": "application/json", accept: "application/json, text/event-stream" },
    body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "tools/call", params: { name, arguments: args } }),
  });
  const raw = await (await (exports as any).default.fetch(tw.withCaller(req, who))).text();
  const data = raw.split("\n").find((l) => l.startsWith("data: "))?.slice(6) ?? raw;
  const res = JSON.parse(data).result;
  return { isError: !!res.isError, body: JSON.parse(res.content[0].text) };
}

describe("mcp template", () => {
  it("a granted tool runs; a tool outside the grant is cap_missing; a limit is enforced", async () => {
    const agent = { tags: ["tag:tw-agent-support"], caps: { [CAP]: [{ tools: ["lookup_order", "refund"], limits: { refund: { maxUsd: 50, perDayUsd: 200 } } }] } };
    expect(await callTool(agent, "lookup_order", { order: "o1" })).toEqual({ isError: false, body: { order: "o1", total: 42, status: "shipped" } });
    const big = await callTool(agent, "refund", { order: "o1", usd: 80 });
    expect(big.isError).toBe(true);
    expect(big.body.code).toBe("cap_limit_exceeded");
    const ok = await callTool(agent, "refund", { order: "o1", usd: 20 });
    expect(ok.body).toMatchObject({ refunded: "o1", usd: 20 });

    const other = { tags: ["tag:other"], caps: { [CAP]: [{ tools: ["lookup_order"] }] } };
    const denied = await callTool(other, "refund", { order: "o1", usd: 5 });
    expect(denied.isError).toBe(true);
    expect(denied.body.code).toBe("cap_missing");
  });
});
