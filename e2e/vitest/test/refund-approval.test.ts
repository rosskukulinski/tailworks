// The refund-approval template (Agents SDK + a Workflow) under
// @cloudflare/vitest-plugin. docs/testing.md.
import { exports } from "cloudflare:workers";
import { describe, it, expect } from "vitest";
import { createTestTailnet } from "@tailworks/tailnet/testing";

const tw = createTestTailnet();

describe("refund-approval template", () => {
  it("a refund request starts a Workflow that waits for approval", async () => {
    const alice = { login: "alice@example.com" };
    const asked = await (exports as any).default.fetch(tw.withCaller(new Request("http://refunds/agents/refund-agent/alice/refund?order=o1&amount=42", { method: "POST" }), alice));
    expect(asked.status).toBe(200);
    const { id } = await asked.json();
    const list = await (await (exports as any).default.fetch(tw.withCaller("http://refunds/agents/refund-agent/alice/refunds", alice))).json();
    expect(list.refunds.find((r: any) => r.id === id)?.state).toBe("waiting for approval");
  });
});
