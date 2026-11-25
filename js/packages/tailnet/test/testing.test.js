// @tailworks/tailnet/testing: withCaller / withPublic and the fake ctx.tailnet.
import { test } from "node:test";
import assert from "node:assert/strict";
import { getCaller, getRequester, principalKey } from "../dist/index.js";
import { createTestTailnet, withCaller, withPublic, enableTestCallers, disableTestCallers } from "../dist/testing.js";

test("withCaller refuses to run outside test mode", () => {
  disableTestCallers();
  assert.throws(() => withCaller("http://app/", { login: "alice@example.com" }), (e) => e.code === "no_identity" && /test helper/.test(e.message));
  assert.throws(() => withPublic("http://app/"), (e) => e.code === "no_identity");
  enableTestCallers();
  assert.equal(getCaller(withCaller("http://app/", { login: "alice@example.com" })).user.login, "alice@example.com");
  disableTestCallers();
});

test("withCaller: a user's node and a tagged node, as getCaller sees them", () => {
  const t = createTestTailnet();
  const alice = getCaller(t.withCaller("http://app/x", { login: "alice@example.com", caps: { "example.com/cap/mcp": [{ tools: ["lookup"] }] } }));
  assert.equal(alice.user.login, "alice@example.com");
  assert.deepEqual(alice.tags, []);
  assert.equal(alice.nodeId, "test-alice");
  assert.equal(alice.node, "alice.test.ts.net");
  assert.deepEqual(alice.caps("example.com/cap/mcp"), [{ tools: ["lookup"] }]);
  assert.equal(principalKey(alice), "alice@example.com");
  const ci = getCaller(t.withCaller(new Request("http://app/", { method: "POST", body: "hi" }), { tags: ["tag:ci"], nodeId: "nCI" }));
  assert.equal(ci.user, undefined);
  assert.deepEqual(ci.tags, ["tag:ci"]);
  assert.equal(ci.nodeId, "nCI");
  assert.equal(principalKey(ci), "nCI");
});

test("withCaller keeps the request and rejects impossible callers", async () => {
  const t = createTestTailnet();
  const r = t.withCaller(new Request("http://app/p?q=1", { method: "POST", body: "body", headers: { "x-a": "1" } }), { login: "bob@example.com", objectId: "room-1" });
  assert.equal(r.method, "POST");
  assert.equal(r.url, "http://app/p?q=1");
  assert.equal(r.headers.get("x-a"), "1");
  assert.equal(await r.text(), "body");
  assert.equal(getCaller(r).objectId, "room-1");
  assert.throws(() => t.withCaller("http://app/", { login: "a@b", tags: ["tag:x"] }), (e) => e.code === "bad_request");
  assert.throws(() => t.withCaller("http://app/", {}), (e) => e.code === "bad_request");
  assert.throws(() => t.withCaller("http://app/", { tags: ["admin"] }), (e) => e.code === "bad_request");
});

test("withPublic: getCaller throws public_request, getRequester says public", () => {
  const t = createTestTailnet();
  const r = t.withPublic(t.withCaller("https://app.example.com/", { login: "alice@example.com" }), { ip: "198.51.100.1" });
  assert.throws(() => getCaller(r), (e) => e.code === "public_request");
  const p = getRequester(r);
  assert.equal(p.kind, "public");
  assert.equal(p.ip, "198.51.100.1");
  assert.equal(p.host, "app.example.com");
  // and back: a caller replaces the public requester
  assert.equal(getCaller(t.withCaller(r, { login: "alice@example.com" })).user.login, "alice@example.com");
});

test("fake ctx.tailnet: spawn, kill, audit, self, fetch", async () => {
  const ns = { getByName: async (n) => ({ stubFor: n }) };
  const t = createTestTailnet({
    env: { LookupAgent: ns },
    bindings: { LookupAgent: "LookupAgent" },
    self: { epoch: 7 },
    fetch: (req) => (new URL(req.url).hostname === "db" ? Response.json({ ok: true }) : new Response("{}", { status: 403, headers: { "tw-error": "peer_not_visible" } })),
  });
  const ctx = t.ctx();
  const child = await ctx.tailnet.spawn(ns, { name: "lookup-1", caps: { "example.com/cap/mcp": { tools: ["lookup"] } }, ttl: "10m" });
  await child.ready;
  assert.deepEqual(child.stub, { stubFor: "lookup-1" });
  assert.equal(child.state(), "ready");
  assert.deepEqual(t.spawned, [{ class: "LookupAgent", name: "lookup-1", caps: { "example.com/cap/mcp": { tools: ["lookup"] } }, ttl: "10m", objectId: "test-lookupagent-lookup-1" }]);
  await assert.rejects(ctx.tailnet.spawn({}, { name: "x" }), (e) => e.code === "bad_request");
  await assert.rejects(ctx.tailnet.spawn(ns, {}), (e) => e.code === "bad_request");
  assert.deepEqual(await ctx.tailnet.kill({ reason: "done", target: { class: "LookupAgent", name: "lookup-1" } }), { killed: ["test-lookupagent-lookup-1"] });
  assert.equal(t.killed[0].reason, "done");
  await ctx.tailnet.audit([{ event: "x" }]);
  assert.deepEqual(t.audits, [{ event: "x" }]);
  const me = await ctx.tailnet.self();
  assert.equal(me.epoch, 7);
  assert.equal(me.workload, "test-workload");
  assert.equal(me.deploymentId, "test-deployment");
  assert.equal(ctx.tailnet.forward, undefined);
  assert.deepEqual(await (await ctx.tailnet.fetch("http://db/items")).json(), { ok: true });
  await assert.rejects(ctx.tailnet.fetch("http://elsewhere/"), (e) => e.code === "peer_not_visible");
  assert.equal(t.fetches.length, 2);
  assert.throws(() => ctx.tailnet.connect("db:5432"), (e) => e.code === "not_supported");
});

test("fake ctx.tailnet.connect: the test's socket", () => {
  const sock = { readable: new ReadableStream(), writable: new WritableStream(), opened: Promise.resolve({ remoteAddress: "db:5432" }), closed: Promise.resolve(), close: async () => {} };
  const seen = [];
  const t = createTestTailnet({ connect: (a, o) => (seen.push([a, o]), sock) });
  assert.equal(t.tailnet.connect({ hostname: "db", port: 5432 }, { tls: "netd" }), sock);
  assert.deepEqual(seen, [[{ hostname: "db", port: 5432 }, { tls: "netd" }]]);
});

test("fake ctx.tailnet: refuses egress by default, onSpawn can fail a spawn", async () => {
  const t = createTestTailnet({ onSpawn: (r) => { if (r.name === "bad") throw Object.assign(new Error("over budget"), { code: "spawn_budget_exceeded" }); } });
  await assert.rejects(t.tailnet.fetch("http://anything/"), (e) => e.code === "forbidden_destination");
  await assert.rejects(t.tailnet.spawn("Agent", { name: "bad" }), (e) => e.code === "spawn_budget_exceeded");
  assert.equal(t.spawned.length, 0);
  const state = t.attach({ storage: {} });
  assert.equal(state.tailnet, t.tailnet);
  let done = false;
  t.ctx().waitUntil(new Promise((r) => setTimeout(() => { done = true; r(); }, 5)));
  await t.settle();
  assert.ok(done);
});
