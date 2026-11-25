// #211: a killed plain DO runs no tenant code through its RPC methods
// (only fetch and __tw_intercept checked the kill marker before).
import { test } from "node:test";
import assert from "node:assert/strict";
import { wrapHandler, wrapPlainClass } from "../dist/runtime.js";

// Base stands in for cloudflare:workers' DurableObject (it keeps ctx and env).
class Base {
  constructor(ctx, env) {
    this.ctx = ctx;
    this.env = env;
  }
  inherited() {
    this.ran.push("inherited");
    return "base";
  }
}

class Room extends Base {
  #secret = "s3";
  ran = [];
  async greet(who) {
    this.ran.push("greet");
    return "hi " + who + " " + this.#secret;
  }
  count() {
    this.ran.push("count");
    return this.ran.length;
  }
  get status() {
    this.ran.push("status");
    return "ok";
  }
  self() {
    return this;
  }
  async fetch() {
    this.ran.push("fetch");
    return new Response("fetched");
  }
  async alarm() {
    this.ran.push("alarm");
  }
  async webSocketMessage() {
    this.ran.push("webSocketMessage");
  }
}

function plainCtx(name, kv) {
  return { id: { name }, storage: { kv: { get: (k) => kv.get(k), put: (k, v) => kv.set(k, v) } }, waitUntil() {} };
}

const tw = { async call(op) { return op === "admit" ? { ok: true, value: { ok: true, existing: true } } : { ok: true, value: {} }; } };

const isKilled = (e) => /^\[killed\] object Room\/r1 was killed: test kill/.test(String(e?.message));

test("a live plain DO's RPC methods, getters and private fields work through the wrapper", async () => {
  const W = wrapPlainClass(Room, "Room", {}, { node: false });
  const obj = new W(plainCtx("r1", new Map()), { __TW: tw });
  assert.equal(await obj.greet("a"), "hi a s3");
  assert.equal(obj.inherited(), "base");
  assert.equal(obj.status, "ok");
  assert.equal(obj.count(), 4);
  assert.equal(await (await obj.fetch(new Request("http://x/"))).text(), "fetched");
});

test("a killed plain DO's RPC methods throw [killed] and run no tenant code (kept stub and fresh stub)", async () => {
  const W = wrapPlainClass(Room, "Room", {}, { node: true });
  const kv = new Map();
  const kept = new W(plainCtx("r1", kv), { __TW: tw });
  assert.equal(await kept.greet("a"), "hi a s3");
  const ran = kept.ran;
  ran.length = 0;
  assert.equal(await kept.__tw_kill("test kill"), true);

  // A stub kept from before the kill reaches the same instance.
  await assert.rejects(async () => kept.greet("b"), isKilled);
  assert.throws(() => kept.count(), isKilled);
  assert.throws(() => kept.inherited(), isKilled);
  assert.throws(() => kept.status, isKilled);
  await kept.alarm(); // lifecycle handlers do nothing
  await kept.webSocketMessage({}, "m");
  const r = await kept.fetch(new Request("http://x/"));
  assert.equal(r.status, 410);
  assert.equal(r.headers.get("tw-error"), "killed");
  assert.deepEqual(ran, [], "tenant code ran after the kill");

  // A fresh stub after the kill: the object is constructed again over the
  // same storage (the marker persists).
  const fresh = new W(plainCtx("r1", kv), { __TW: tw });
  await assert.rejects(async () => fresh.greet("c"), isKilled);
  assert.throws(() => fresh.count(), isKilled);
  assert.deepEqual(fresh.ran, [], "tenant code ran after the kill");
});

test("an RPC call into a killed plain DO answers the caller's request with a typed 410", async () => {
  const W = wrapPlainClass(Room, "Room", {}, { node: false });
  const kv = new Map();
  const obj = new W(plainCtx("r1", kv), { __TW: tw });
  await obj.__tw_kill("test kill");
  // workerd RPC: the callee's exception crosses as an Error with its message.
  const rpc = (o) =>
    new Proxy({}, {
      get(_t, p) {
        if (p === "then") return undefined;
        return async (...a) => {
          try {
            return await o[p](...a);
          } catch (e) {
            throw new Error(e.message);
          }
        };
      },
    });
  const ns = { getByName: () => rpc(obj), idFromName: (n) => ({ name: n }), get: () => rpc(obj) };
  const H = wrapHandler({ async fetch(_req, env) { return new Response(await env.ROOM.getByName("r1").greet("d")); } }, { ROOM: "Room" });
  const res = await H.fetch({ url: "https://wl.example.ts.net/", headers: new Headers(), cf: { tw: { kind: "workload" } } }, { ROOM: ns, __TW: tw }, {});
  assert.equal(res.status, 410, await res.clone().text());
  assert.equal(res.headers.get("tw-error"), "killed");
  assert.deepEqual(obj.ran, []);
});

test("methods called inside a live object keep their identity (decorator metadata)", () => {
  const W = wrapPlainClass(Room, "Room", {}, { node: false });
  const obj = new W(plainCtx("r1", new Map()), { __TW: tw });
  const inner = obj.self();
  assert.equal(inner.greet, Room.prototype.greet);
  assert.ok(inner instanceof Room);
});
