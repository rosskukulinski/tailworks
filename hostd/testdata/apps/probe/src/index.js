// Test fixture for tw-hostd: an ordinary Worker + DOs that expose what the
// platform did (caller, attribution, headers) so the Go tests can check it.
import { DurableObject, env as rawEnv } from "cloudflare:workers";
import { getCaller, tailnet, isTailnetError } from "@tailworks/tailnet";

// A service binding's wire header (hostd.HeaderBindingWire), as tenant code
// might forge it.
const WIRE = "v1; entrypoint=Admin";

const SEEN = (req) => Object.fromEntries([...req.headers].filter(([k]) => k.startsWith("x-tw-") || k.startsWith("cf-") || k === "x-forwarded-for" || k === "tailnet-assertion"));

async function egress(url, init) {
  try {
    const r = await fetch(url, init);
    const h = { "content-type": r.headers.get("content-type") || "text/plain" };
    if (r.headers.get("tw-error")) h["tw-error"] = r.headers.get("tw-error");
    return new Response(await r.text(), { status: r.status, headers: h });
  } catch (e) {
    return Response.json({ tenantCaught: String(e) }, { status: 599 });
  }
}

function stream() {
  const enc = new TextEncoder();
  let n = 0;
  return new Response(new ReadableStream({
    async pull(c) {
      await new Promise((r) => setTimeout(r, 50));
      c.enqueue(enc.encode("tick " + n++ + "\n"));
    },
  }));
}

let boots = 0;

// hostRPC: tenant code calls a facet Host's internal member (m) over RPC on
// ns.getByName(name). ?then=1 calls hello() on the result (pipelined), as
// tenant code would on a facet stub it got back. A refusal is a 403.
async function hostRPC(ns, url) {
  const q = (k) => url.searchParams.get(k);
  try {
    const stub = ns.getByName(q("name"));
    const p = q("get") ? stub[q("m")] : stub[q("m")](...JSON.parse(q("args") ?? "[]"));
    const v = q("then") ? await p.hello("bypass") : await p;
    let out = null;
    try {
      out = JSON.parse(JSON.stringify(v ?? null));
    } catch {
      out = String(v);
    }
    return Response.json({ reached: true, value: out });
  } catch (e) {
    return Response.json({ refused: String(e?.message ?? e) }, { status: 403 });
  }
}

export class Probe extends DurableObject {
  constructor(ctx, env) {
    super(ctx, env);
    boots++;
  }
  async hello(x) {
    return "hello " + x + " from " + this.ctx.id.name;
  }
  async fetch(req) {
    const url = new URL(req.url);
    const q = (k) => url.searchParams.get(k);
    switch (url.pathname) {
      case "/egress":
        return egress(q("url"));
      case "/tegress":
        try {
          const r = await tailnet(this.ctx).fetch(q("url"));
          return new Response(await r.text(), { status: r.status });
        } catch (e) {
          return Response.json({ thrown: isTailnetError(e), code: e.code, message: e.message }, { status: 299 });
        }
      case "/forge-egress": {
        const victim = q("as");
        return egress(q("url"), {
          headers: { "x-tw-object": victim, "x-tw-class": "Probe", "x-tw-name": victim, "x-tw-mac": "00", "x-tw-cf": JSON.stringify({ objectId: victim }), "Proxy-Authorization": victim, "tailworks-binding": WIRE },
          cf: { objectId: victim },
        });
      }
      case "/stream":
        return stream();
      // DO storage (KV API and SQLite): persistence across VM restarts and
      // redeploys (L3), and write latency.
      case "/kv/put":
        await this.ctx.storage.put(q("k"), q("v"));
        return Response.json({ ok: true });
      case "/kv/get":
        return Response.json({ k: q("k"), v: (await this.ctx.storage.get(q("k"))) ?? null });
      case "/sql/put": {
        const sql = this.ctx.storage.sql;
        sql.exec("CREATE TABLE IF NOT EXISTS kv (k TEXT PRIMARY KEY, v TEXT)");
        sql.exec("INSERT OR REPLACE INTO kv VALUES (?, ?)", q("k"), q("v"));
        return Response.json({ ok: true });
      }
      case "/sql/get": {
        const sql = this.ctx.storage.sql;
        sql.exec("CREATE TABLE IF NOT EXISTS kv (k TEXT PRIMARY KEY, v TEXT)");
        const rows = sql.exec("SELECT v FROM kv WHERE k = ?", q("k")).toArray();
        return Response.json({ k: q("k"), v: rows.length ? rows[0].v : null });
      }
      case "/env":
        return Response.json({ env: Object.keys(this.env).sort(), props: this.ctx.props ?? null, hasTailnet: !!this.ctx.tailnet });
      case "/spawn": {
        const child = await tailnet(this.ctx).spawn(this.env.PROBE, { name: q("name"), caps: q("caps") ? JSON.parse(q("caps")) : undefined, ttl: "10m" });
        const before = child.state();
        try {
          await child.ready;
        } catch (e) {
          return Response.json({ error: e.code, message: e.message, before }, { status: 299 });
        }
        return Response.json({ objectId: child.objectId, name: child.name, before, after: child.state(), reply: await child.stub.hello("parent") });
      }
      case "/spawn-call": {
        // Spawn a child and have it call ?url at once, without waiting for
        // it to be ready: its first egress races its ready report (#331).
        let child;
        try {
          child = await tailnet(this.ctx).spawn(this.env.PROBE, { name: q("name"), caps: JSON.parse(q("caps")), ttl: "10m" });
        } catch (e) {
          return Response.json({ spawn: e.code ?? String(e), message: e.message }, { status: 298 });
        }
        const r = await child.stub.fetch(new Request("http://child/tegress?url=" + encodeURIComponent(q("url"))));
        return new Response(await r.text(), { status: r.status });
      }
      case "/audit": {
        // RT-02: tenant code tries to write a platform-looking "allow"
        // record that frames a human and re-points the trace.
        await tailnet(this.ctx).audit([{ kind: q("kind") ?? "allow", grant: "example.com/cap/mcp: refund", actingFor: "ceo@victim.example", traceID: "trace-of-victim", detail: "wired $1M" }]);
        return Response.json({ ok: true });
      }
      case "/kill-self":
        return Response.json(await tailnet(this.ctx).kill({ reason: "self" }));
      case "/sibling":
        return Response.json({ reply: await this.env.PROBE.getByName(q("name")).hello("sibling") });
      case "/hostrpc":
        return hostRPC(this.env.PROBE, url);
      case "/ticker":
        return this.env.TICKER.getByName(q("name")).fetch(new Request("http://t/" + (q("op") ?? "")));
    }
    return Response.json({
      kind: "probe",
      name: this.ctx.id.name,
      boots,
      caller: req.cf?.tailscale?.caller ?? null,
      objectId: req.cf?.objectId ?? null,
      sawHeaders: SEEN(req),
    });
  }
}

// Plain-DO mode ("egress": "workload"): alarms work.
export class Ticker extends DurableObject {
  async fetch(req) {
    const url = new URL(req.url);
    if (url.pathname === "/arm") {
      await this.ctx.storage.setAlarm(Date.now() + 100);
      return Response.json({ armed: true });
    }
    if (url.pathname === "/egress") return egress(url.searchParams.get("url"));
    return Response.json({ kind: "ticker", name: this.ctx.id.name, alarms: (await this.ctx.storage.get("alarms")) ?? 0, hasTailnet: !!this.ctx.tailnet, caller: req.cf?.tailscale?.caller ?? null });
  }
  async alarm() {
    await this.ctx.storage.put("alarms", ((await this.ctx.storage.get("alarms")) ?? 0) + 1);
  }
  // An RPC method (#211: a killed Ticker must not run it).
  async hello(x) {
    return { hello: x, name: this.ctx.id.name };
  }
}

export default {
  async fetch(req, env, ctx) {
    const url = new URL(req.url);
    // ?wire=1: with the binding wire header, which is hostd's to set (#347).
    if (url.pathname === "/egress") return egress(url.searchParams.get("url"), url.searchParams.get("wire") ? { headers: { "tailworks-binding": WIRE } } : undefined);
    if (url.pathname.startsWith("/probe/")) {
      const [, , name, ...rest] = url.pathname.split("/");
      const u = new URL(req.url);
      u.pathname = "/" + rest.join("/");
      return env.PROBE.getByName(name).fetch(new Request(u, req));
    }
    // ?raw=1: the raw binding, which admits nothing (it reaches only existing objects).
    if (url.pathname === "/tickrpc") {
      const ns = url.searchParams.get("raw") ? rawEnv.TICKER : env.TICKER;
      return Response.json({ reply: await ns.getByName(url.searchParams.get("name")).hello("worker") });
    }
    // The raw binding is the platform's Host_Probe namespace itself.
    if (url.pathname === "/hostrpc") return hostRPC(rawEnv.PROBE, url);
    if (url.pathname === "/rpc") return Response.json({ reply: await env.PROBE.getByName(url.searchParams.get("name")).hello("worker") });
    return Response.json({ kind: "worker", caller: getCaller(req), sawHeaders: SEEN(req), hasTailnet: !!ctx.tailnet, env: Object.keys(env).sort() });
  },
};
