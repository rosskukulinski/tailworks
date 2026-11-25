// The lock template under @cloudflare/vitest-plugin: its Worker and Durable
// Object run inside workerd as on Cloudflare; @tailworks/tailnet/testing supplies
// the tailnet callers and a fake ctx.tailnet. docs/testing.md.
import { env, exports } from "cloudflare:workers";
import { runInDurableObject, runDurableObjectAlarm } from "cloudflare:test";
import { describe, it, expect } from "vitest";
import { createTestTailnet } from "@tailworks/tailnet/testing";

const tw = createTestTailnet();
const alice = { login: "alice@example.com" };
const bob = { login: "bob@example.com" };
const LOCK = (env as any).LOCK;

const post = (path: string, who: object, body: object = {}) =>
  (exports as any).default.fetch(tw.withCaller(new Request("http://locks" + path, { method: "POST", body: JSON.stringify(body) }), who));

describe("lock template", () => {
  it("without a caller the template answers 401 no_identity", async () => {
    const r = await (exports as any).default.fetch("http://locks/locks/a");
    expect(r.status).toBe(401);
    expect((await r.json()).error).toBe("no_identity");
  });

  it("withCaller reaches getCaller through the Worker and the Durable Object stub", async () => {
    const got = await post("/locks/b/acquire", alice);
    expect(got.status).toBe(200);
    const busy = await post("/locks/b/acquire", bob);
    expect(busy.status).toBe(409);
    const held = await busy.json();
    expect(held.holder.user).toBe("alice@example.com");
    expect(held.holder.nodeId).toBe("test-alice");
  });

  it("Durable Object storage and alarms: an expired lease is freed by the alarm", async () => {
    const stub = LOCK.getByName("c");
    expect((await post("/locks/c/acquire", alice, { ttlMs: 100 })).status).toBe(200);
    await new Promise((r) => setTimeout(r, 150));
    await runDurableObjectAlarm(stub); // false if workerd already ran it
    const s: any = await runInDurableObject(stub, (_o: unknown, state: any) => state.storage.get("state"));
    expect(s.holder).toBe(null);
    expect(s.why).toBe("expired");
  });

  it("a fake ctx.tailnet on the object: self().epoch feeds the fencing token, audits are recorded", async () => {
    const stub = LOCK.getByName("d");
    const fake = createTestTailnet({ self: { epoch: 7 } });
    await runInDurableObject(stub, (_o: unknown, state: any) => {
      fake.attach(state);
    });
    const r = await post("/locks/d/acquire", alice);
    expect((await r.json()).token).toBe(7_000_000_001);
    expect(fake.audits).toMatchObject([{ event: "lock_acquired", lock: "d", holderNodeId: "test-alice" }]);
  });
});
