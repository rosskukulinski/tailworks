import { test } from "node:test";
import assert from "node:assert/strict";
import { TailnetError, isTailnetError, fromResponse, CATALOG, tailnet } from "../dist/index.js";
import { createTailnetContext, wrapHandler, wrapFacetClass } from "../dist/runtime.js";

test("TailnetError wire shape and catalog statuses", async () => {
  const e = new TailnetError("cap_missing", "tool refund not granted", { missing: { cap: "x", tool: "refund" }, snippet: "{}" });
  assert.equal(e.status, 403);
  assert.equal(e.docUrl.endsWith("#cap_missing"), true);
  assert.deepEqual(e.toJSON().missing, { cap: "x", tool: "refund" });
  const r = e.toResponse();
  assert.equal(r.status, 403);
  assert.equal(r.headers.get("tw-error"), "cap_missing");
  const back = await fromResponse(r);
  assert.equal(back.code, "cap_missing");
  assert.equal(back.detail, "tool refund not granted");
  assert.ok(isTailnetError(back));
  assert.equal(await fromResponse(new Response("ok")), null);
  for (const [code, ent] of Object.entries(CATALOG)) assert.ok(ent.status >= 400 && ent.title, code);
  assert.equal(isTailnetError({ name: "TailnetError", code: "x" }), true);
});

test("tailnet(ctx) throws no_identity off-platform", () => {
  assert.throws(() => tailnet({}), (e) => e.code === "no_identity");
});

function fakeTw(log) {
  return {
    async call(op, args) {
      log.push([op, args]);
      if (op === "spawn") return { ok: true, value: { objectID: "o1", status: "pending", hostname: "app-lookup-x" } };
      if (op === "ready") return { ok: true, value: { magicDNS: "app-lookup-x.ts.net" } };
      if (op === "kill") return { ok: false, error: { code: "killed", message: "nope" } };
      return { ok: true, value: {} };
    },
  };
}

test("ctx.tailnet.spawn resolves the binding's class and passes self", async () => {
  const log = [];
  const ns = { getByName: (n) => ({ name: n }) };
  const env = { LOOKUP: ns };
  const tn = createTailnetContext(fakeTw(log), env, { LOOKUP: "LookupAgent" }, { class: "Plain", name: "p1" });
  const sp = await tn.spawn(env.LOOKUP, { name: "x", caps: { a: 1 } });
  assert.equal(sp.objectId, "o1");
  assert.deepEqual(sp.stub, { name: "x" });
  assert.equal(sp.state(), "pending");
  await sp.ready;
  assert.equal(sp.state(), "ready");
  assert.equal(sp.name, "app-lookup-x.ts.net");
  assert.deepEqual(log[0], ["spawn", { class: "LookupAgent", name: "x", caps: { a: 1 }, ttl: undefined, principal: undefined, as: { class: "Plain", name: "p1" } }]);
  await assert.rejects(tn.spawn({}, { name: "y" }), (e) => e.code === "bad_request");
  await assert.rejects(tn.kill(), (e) => isTailnetError(e) && e.code === "killed");
  // forward left the TailnetContext type but stays, unlisted, for apps
  // deployed against the old SDK (one minor release).
  assert.equal(typeof tn.forward, "function");
});

test("ctx.tailnet.fetch throws typed errors for tw-error responses", async () => {
  const orig = globalThis.fetch;
  globalThis.fetch = async () => new Response(JSON.stringify({ code: "peer_not_visible", detail: "no peer named db" }), { status: 403, headers: { "tw-error": "peer_not_visible" } });
  try {
    const tn = createTailnetContext(fakeTw([]), {}, {});
    await assert.rejects(tn.fetch("http://db/"), (e) => e.code === "peer_not_visible" && e.status === 403 && /no peer/.test(e.message));
  } finally {
    globalThis.fetch = orig;
  }
});

test("wrapHandler installs ctx.tailnet; wrapFacetClass hides __TW", async () => {
  const H = wrapHandler({ async fetch(req, env, ctx) { return typeof ctx.tailnet.spawn; } }, {});
  assert.equal(await H.fetch({}, { __TW: fakeTw([]) }, {}), "function");
  class Base { constructor(ctx, env) { this.ctx = ctx; this.env = env; } }
  const W = wrapFacetClass(Base, "Counter", {});
  assert.equal(W.name, "Counter");
  const w = new W({}, { __TW: fakeTw([]), X: 1 });
  assert.equal(w.env.__TW, undefined);
  assert.equal(w.env.X, 1);
  assert.equal(typeof w.ctx.tailnet.fetch, "function");
});

test("wrapHandler keeps handler methods of a class instance (export default new OAuthProvider(...))", async () => {
  class Provider {
    constructor() { this.n = 0; }
    async fetch(req, env, ctx) { this.n++; return `${this.n}:${typeof ctx.tailnet.spawn}`; }
    scheduled() { return "cron"; }
    helper() { return "not a handler"; }
  }
  const p = new Provider();
  const H = wrapHandler(p, {});
  assert.equal(typeof H.fetch, "function");
  assert.equal(typeof H.scheduled, "function");
  assert.equal(H.helper, undefined);
  assert.equal(await H.fetch({}, { __TW: fakeTw([]) }, {}), "1:function");
  assert.equal(p.n, 1); // called with the instance as this
});

// tcpTw is a TailnetApi stub whose tcpConnect answers with an echo socket
// (what it got, then what is written) or refuses with code.
function tcpTw(log, code) {
  return {
    async call() {
      return { ok: true, value: {} };
    },
    async tcpConnect(args) {
      log.push(args);
      if (code) return { ok: false, error: { code, message: "refused " + args.address } };
      const t = new TransformStream();
      const w = t.writable.getWriter();
      w.write(new TextEncoder().encode("hello " + args.address + "\n"));
      w.releaseLock();
      return { ok: true, value: { readable: t.readable, writable: t.writable } };
    },
  };
}

test("ctx.tailnet.connect: streams from tcpConnect, typed refusals, plain-DO as", async () => {
  const log = [];
  const tn = createTailnetContext(tcpTw(log), {}, {});
  const s = tn.connect({ hostname: "db", port: 5432 });
  assert.deepEqual(await s.opened, { remoteAddress: "db:5432" });
  const w = s.writable.getWriter();
  await w.write(new TextEncoder().encode("ping\n"));
  const r = s.readable.getReader();
  let got = "";
  while (!got.includes("ping")) got += new TextDecoder().decode((await r.read()).value);
  assert.equal(got, "hello db:5432\nping\n");
  await s.close();
  assert.deepEqual(log, [{ address: "db:5432" }]);

  // IPv6 hosts are bracketed; tls passes through.
  tn.connect({ hostname: "fd7a::1", port: 5432 }, { tls: "netd" });
  await new Promise((r) => setTimeout(r, 0));
  assert.deepEqual(log[1], { address: "[fd7a::1]:5432", tls: "netd" });

  // A plain-DO object's context sends its attested-by-claim `as` (the platform refuses it).
  const plain = createTailnetContext(tcpTw(log), {}, {}, { class: "Plain", name: "p1" });
  plain.connect("db:5432");
  await new Promise((r) => setTimeout(r, 0));
  assert.deepEqual(log[2], { address: "db:5432", as: { class: "Plain", name: "p1" } });

  const refused = createTailnetContext(tcpTw([], "peer_not_visible"), {}, {}).connect("nope:1");
  await assert.rejects(refused.opened, (e) => isTailnetError(e) && e.code === "peer_not_visible");
  await assert.rejects(refused.closed, (e) => e.code === "peer_not_visible");

  assert.throws(() => tn.connect("db:5432", { secureTransport: "on" }), (e) => e.code === "not_supported");
  assert.throws(() => tn.connect({ hostname: "db" }), (e) => e.code === "bad_request");
});
