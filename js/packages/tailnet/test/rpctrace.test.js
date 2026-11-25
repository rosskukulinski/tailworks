// #295: trace context across DO RPC, alarms, cron runs and ctx.tailnet.fetch.
import { test } from "node:test";
import assert from "node:assert/strict";
import { currentTrace, runTraced } from "../dist/trace.js";
import { takeMark } from "../dist/rpctrace.js";

// Egress: the fake network answers with the traceparent it saw. Installed
// before the runtime loads, which wraps globalThis.fetch once.
globalThis.fetch = async (input, init) => new Response("ok", { headers: { "x-saw": new Headers(init?.headers ?? input?.headers).get("traceparent") ?? "" } });
const { wrapHandler, wrapPlainClass, wrapFacetClass, createTailnetContext } = await import("../dist/runtime.js");

const T = "4bf92f3577b34da6a3ce929d0e0e4736";
const P = "00f067aa0ba902b7";
const IN = `00-${T}-${P}-01`;

function fakeHostd(log) {
  return {
    async call(op, args) {
      log.push([op, args]);
      if (op === "admit") return { ok: true, value: { ok: true, existing: true } };
      return { ok: true, value: {} };
    },
  };
}

class DO {
  constructor(ctx, env) {
    this.ctx = ctx;
    this.env = env;
  }
  async ping(...args) {
    const tc = currentTrace();
    const r = await fetch("http://peer/from-rpc");
    return { args, trace: tc?.traceId, span: tc?.spanId, egress: r.headers.get("x-saw") };
  }
  async alarm() {
    const r = await fetch("http://peer/from-alarm");
    this.seen = { tc: currentTrace(), egress: r.headers.get("x-saw") };
  }
}

function waits() {
  const ps = [];
  return { ctx: { waitUntil: (p) => ps.push(p) }, settle: () => Promise.all(ps) };
}

function plainCtx(name) {
  const kv = new Map();
  const w = waits();
  return { ctx: { id: { name, toString: () => "id-" + name }, storage: { kv: { get: (k) => kv.get(k), put: (k, v) => kv.set(k, v) } }, waitUntil: w.ctx.waitUntil }, settle: w.settle };
}

// withEgress marks the tests whose egress goes to the fake network above.
async function withEgress(fn) {
  return fn();
}

test("takeMark strips only the runtime's marker", () => {
  const a = ["x", { __tw_tp: IN }];
  assert.equal(takeMark(a), IN);
  assert.deepEqual(a, ["x"]);
  for (const other of [[{ __tw_tp: IN, more: 1 }], [{ __tw_tp: 1 }], [new (class { constructor() { this.__tw_tp = IN; } })()], [], ["x"]]) {
    const before = other.length;
    assert.equal(takeMark(other), undefined);
    assert.equal(other.length, before);
  }
});

test("DO RPC: a CLIENT span in the caller, a SERVER span in the plain DO, one trace", async () => {
  await withEgress(async () => {
    const log = [];
    const tw = fakeHostd(log);
    const Room = wrapPlainClass(DO, "Room", { ROOM: "Room" }, { node: false });
    const pc = plainCtx("r1");
    const obj = new Room(pc.ctx, { __TW: tw });
    // What workerd's stub reaches: the object the constructor returned.
    const env = { ROOM: { getByName: () => obj, idFromName: (n) => ({ name: n }) }, __TW: tw };
    const H = wrapHandler({ async fetch(req, env) { return Response.json(await env.ROOM.getByName("r1").ping("a", 2)); } }, { ROOM: "Room" });
    const w = waits();
    const r = await (await H.fetch(new Request("http://wl/x", { headers: { traceparent: IN } }), env, w.ctx)).json();
    await w.settle();
    await pc.settle();
    assert.deepEqual(r.args, ["a", 2]); // the marker never reaches tenant code
    assert.equal(r.trace, T);
    const reports = log.filter((l) => l[0] === "spans").map((l) => l[1]);
    const all = reports.flatMap((x) => x.spans);
    const client = all.find((s) => s.name === "Room ping" && !s.kind);
    const server = all.find((s) => s.name === "Room ping" && s.kind === "server");
    assert.ok(client && server, JSON.stringify(all));
    assert.equal(client.parentSpanId, P);
    assert.equal(server.parentSpanId, client.spanId);
    assert.equal(server.spanId, r.span);
    assert.equal(server.traceId, T);
    assert.equal(r.egress, `00-${T}-${server.spanId}-01`); // egress inside the method joins
    assert.deepEqual(server.attrs, { "tw.do.class": "Room", "tw.do.name": "r1", "tw.do.id": "id-r1", "tw.trigger": "rpc", "rpc.method": "ping" });
    // The callee's report names the plain DO.
    assert.deepEqual(reports.find((x) => x.spans.includes(server)).as, { class: "Room", name: "r1" });
  });
});

test("an RPC call outside any trace passes no marker", async () => {
  const seen = [];
  const env = { ROOM: { getByName: () => ({ ping: (...a) => seen.push(a) }) }, __TW: fakeHostd([]) };
  const H = wrapHandler({ async fetch(req, env) { await env.ROOM.getByName("r1").ping("a"); return new Response("ok"); } }, { ROOM: "Room" });
  await H.fetch(new Request("http://wl/x"), env, {});
  assert.deepEqual(seen, [["a"]]);
});

test("a plain DO's alarm starts a trace; its egress joins it", async () => {
  await withEgress(async () => {
    const log = [];
    const Room = wrapPlainClass(DO, "Room", {}, { node: false });
    const pc = plainCtx("r2");
    const obj = new Room(pc.ctx, { __TW: fakeHostd(log) });
    await obj.alarm();
    await pc.settle();
    const sp = log.filter((l) => l[0] === "spans").flatMap((l) => l[1].spans);
    assert.equal(sp.length, 1);
    assert.equal(sp[0].name, "Room alarm");
    assert.equal(sp[0].kind, "server");
    assert.equal(sp[0].parentSpanId, "");
    assert.equal(sp[0].attrs["tw.trigger"], "alarm");
    assert.equal(obj.seen.egress, `00-${sp[0].traceId}-${sp[0].spanId}-01`);
  });
});

test("a facet's delegated alarm and RPC are traced", async () => {
  await withEgress(async () => {
    const log = [];
    const W = wrapFacetClass(DO, "Counter", {});
    const w = waits();
    const obj = new W({ id: { name: "c1" }, storage: {}, waitUntil: w.ctx.waitUntil }, { __TW: fakeHostd(log) });
    await obj.__tw_alarm({ retryCount: 0 });
    const r = await obj.ping("z", { __tw_tp: IN });
    await w.settle();
    assert.deepEqual(r.args, ["z"]);
    assert.equal(r.trace, T);
    const sp = log.filter((l) => l[0] === "spans").flatMap((l) => l[1].spans);
    assert.deepEqual(sp.map((s) => [s.name, s.kind, s.parentSpanId]), [["Counter alarm", "server", ""], ["Counter ping", "server", P]]);
    assert.equal(sp[0].attrs["tw.do.id"], undefined); // a facet is attested by its binding instead
    assert.equal(obj.seen.egress, `00-${sp[0].traceId}-${sp[0].spanId}-01`);
  });
});

test("a cron run runs in hostd's root span: its egress joins", async () => {
  await withEgress(async () => {
    let saw;
    const H = wrapHandler({ fetch: () => new Response("no"), async scheduled() { saw = (await fetch("http://peer/cron")).headers.get("x-saw"); } }, {});
    const req = new Request("http://tenant/_tw/scheduled", { method: "POST", headers: { traceparent: IN } });
    Object.defineProperty(req, "cf", { value: { tw: { kind: "scheduled", cron: "* * * * *", scheduledTime: Date.now() } } });
    const r = await (await H.fetch(req, { __TW: fakeHostd([]) }, {})).json();
    assert.equal(r.outcome, "ok");
    assert.equal(saw, IN);
  });
});

test("ctx.tailnet.fetch joins the caller's trace", async () => {
  await withEgress(async () => {
    const tn = createTailnetContext(fakeHostd([]), {}, {});
    const r = await runTraced(new Request("http://x/", { headers: { traceparent: IN } }), () => tn.fetch("http://db/"));
    assert.equal(r.headers.get("x-saw"), IN);
  });
});
