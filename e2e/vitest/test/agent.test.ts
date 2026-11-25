// The agent template (Cloudflare Agents SDK) under @cloudflare/vitest-plugin,
// with @tailworks/tailnet/testing for callers and a fake ctx.tailnet.spawn.
// docs/testing.md.
import { env, exports } from "cloudflare:workers";
import { runInDurableObject } from "cloudflare:test";
import { describe, it, expect } from "vitest";
import { createTestTailnet } from "@tailworks/tailnet/testing";

const tw = createTestTailnet();
const get = async (path: string, who: object) => (await (exports as any).default.fetch(tw.withCaller("http://agent" + path, who))).json();

describe("agent template", () => {
  it("Agents SDK state and SQL: one agent per caller", async () => {
    const alice = { login: "alice@example.com" };
    const a1 = await get("/", alice);
    const a2 = await get("/", alice);
    expect(a2.agent).toBe("alice@example.com");
    expect(a2.state.visits).toBe(a1.state.visits + 1);
    expect(a2.calls).toBe(a1.calls + 1);
    const ci = await get("/", { tags: ["tag:ci"], nodeId: "nCI" });
    expect(ci.agent).toBe("nCI"); // a tagged caller is keyed by its node ID
    expect(ci.caller.tags).toEqual(["tag:ci"]);
    expect(ci.state.visits).toBe(1);
  });

  it("ctx.tailnet.spawn with the fake runtime: the spawn is recorded and the child stub is real", async () => {
    const fake = createTestTailnet();
    await runInDurableObject((env as any).SupportAgent.getByName("helper"), (_o: unknown, state: any) => {
      fake.attach(state);
    });
    const r = await get("/agents/support-agent/helper/spawn?child=lookup-1", { login: "alice@example.com" });
    expect(fake.spawned).toMatchObject([{ name: "lookup-1", caps: { "example.com/cap/mcp": { tools: ["lookup_order"] } }, ttl: "10m" }]);
    expect(r.child.reply).toMatchObject({ lookup: "lookup-1", parent: "helper", lookups: 1 });
  });

  it("without the fake runtime, ctx.tailnet is absent (as off tw)", async () => {
    const r = await (exports as any).default.fetch(tw.withCaller("http://agent/agents/support-agent/plain/spawn", { login: "alice@example.com" }));
    expect(r.status).toBe(500);
  });
});
