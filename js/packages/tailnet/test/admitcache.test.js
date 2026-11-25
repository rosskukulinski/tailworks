// #153 (ledger H19): the isolate's admit cache must not let a call skip
// hostd's admit after the object is killed and purged.
import { test } from "node:test";
import assert from "node:assert/strict";
import { wrapHandler, wrapPlainClass } from "../dist/runtime.js";

const pub = (o) => ({
  url: "https://wl.example.ts.net/x?o=" + o,
  headers: new Headers(),
  cf: { traceId: "t1", tailscale: { requester: { kind: "public", ip: "203.0.113.7" } } },
});
const tailnet = (o, ticket) => ({
  url: "https://wl.example.ts.net/x?o=" + o,
  headers: new Headers(),
  cf: { traceId: "t2", tw: { kind: "workload", ...(ticket ? { ticket } : {}) }, tailscale: { caller: { nodeId: "nX", tags: [] } } },
});

function fakeNS(log) {
  const stub = (name) => ({
    id: { name },
    name,
    async fetch() {
      log.push(["fetch", name]);
      return new Response("hello " + name);
    },
  });
  return { getByName: (n) => stub(n), idFromName: (n) => ({ name: n }), get: (id) => stub(id.name) };
}

// fakeHostd models hostd: live names admit as existing; a ticket admits a
// new name (one-shot); anything else is refused.
function fakeHostd(log, live) {
  return {
    async call(op, args) {
      log.push([op, args]);
      if (op === "admit") {
        if (live.has(args.name)) return { ok: true, value: { ok: true, existing: true } };
        if (typeof args.ticket === "string") return { ok: true, value: { ok: true, existing: false } };
        return { ok: false, error: { code: "create_not_admitted", message: "no admission for " + args.name } };
      }
      return { ok: true, value: {} };
    },
  };
}

const handler = {
  async fetch(req, env) {
    return env.ROOM.getByName(new URL(req.url).searchParams.get("o")).fetch(req);
  },
};

// DO stands in for cloudflare:workers' DurableObject (it keeps ctx and env).
class DO {
  constructor(ctx, env) {
    this.ctx = ctx;
    this.env = env;
  }
}

function plainCtx(name, kv = new Map()) {
  return { id: { name }, storage: { kv: { get: (k) => kv.get(k), put: (k, v) => kv.set(k, v) } }, waitUntil() {} };
}

test("a killed plain DO's name is admitted again, not served from the cache", async () => {
  const log = [];
  const live = new Set(["killed-1"]);
  const tw = fakeHostd(log, live);
  const env = { ROOM: fakeNS(log), __TW: tw };
  const H = wrapHandler(handler, { ROOM: "Room" });
  assert.equal(await (await H.fetch(pub("killed-1"), env, {})).text(), "hello killed-1");

  // tw kill (the platform's admin kill reaches the plain DO's __tw_kill
  // in this isolate), then purge: hostd no longer knows the name.
  const Room = wrapPlainClass(DO, "Room", { ROOM: "Room" }, { node: true });
  const obj = new Room(plainCtx("killed-1"), { __TW: tw });
  await obj.__tw_kill("test kill");
  live.delete("killed-1");

  log.length = 0;
  const r = await H.fetch(pub("killed-1"), env, {});
  assert.equal(r.status, 403, await r.clone().text());
  assert.equal(log.filter((l) => l[0] === "admit").length, 1, JSON.stringify(log));
  assert.equal(log.filter((l) => l[0] === "fetch").length, 0, JSON.stringify(log));
});

test("an admission to create is not cached: a later caller without one must admit", async () => {
  const log = [];
  const env = { ROOM: fakeNS(log), __TW: fakeHostd(log, new Set()) };
  const H = wrapHandler(handler, { ROOM: "Room" });
  assert.equal(await (await H.fetch(tailnet("new-1", "v1.9999999999999.t2.mac"), env, {})).text(), "hello new-1");
  log.length = 0;
  // (killed and purged meanwhile:) a request without a ticket.
  const r = await H.fetch(tailnet("new-1"), env, {});
  assert.equal(r.headers.get("tw-error"), "create_not_admitted");
  assert.equal(log.filter((l) => l[0] === "fetch").length, 0, JSON.stringify(log));
});

test("a killed plain DO asks hostd for no node when it is constructed again", async () => {
  const log = [];
  const tw = fakeHostd(log, new Set(["k2"]));
  const Room = wrapPlainClass(DO, "Room", { ROOM: "Room" }, { node: true });
  const kv = new Map();
  const first = new Room(plainCtx("k2", kv), { __TW: tw });
  assert.equal(log.filter((l) => l[0] === "ensure").length, 1);
  await first.__tw_kill("test kill");
  log.length = 0;
  new Room(plainCtx("k2", kv), { __TW: tw });
  assert.equal(log.filter((l) => l[0] === "ensure").length, 0, JSON.stringify(log));
});
