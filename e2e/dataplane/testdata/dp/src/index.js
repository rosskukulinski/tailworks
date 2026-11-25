// The e2e/dataplane workload (LOOP.md "Workloads"). Every op carries a
// client-side ID; the consumer and Pulse report to the recorder node, so
// the record survives whatever happens to the hosts.
//
//   GET /send?k=<id>        → JOBS.send({k}); 200 {sent}
//                             k ending "-r": retried once, then acked
//                             k ending "-x": always throws (dead-lettered)
//   GET /reg/incr?op=<id>   → Reg DO: counter += 1; 200 {v, epoch}
//   GET /reg/get            → Reg DO: 200 {v, epoch}
//   GET /d1/add?id=<id>     → INSERT INTO ops (id); 200 {id}
//   GET /d1/all             → 200 {ids: [...]}
//   GET /kv/put?k=&v=[&ttl=] → KV.put(k, v[, {expirationTtl}]); 200 {epoch}
//   GET /kv/get?k=          → KV.get(k); 200 {v (null if missing), epoch}
//   GET /clock              → 200 {now, obj}: Date.now() here and in the Reg DO (the skew check)
//   /pulse/start?url=...    → Pulse DO: an egress call every 250 ms
//   /locks/...              → the lock template
import { DurableObject } from "cloudflare:workers";
import lockWorker from "../../../../../js/packages/create-tailworks/templates/lock/src/index";
export { Lock } from "../../../../../js/packages/create-tailworks/templates/lock/src/lock";

async function epochOf(ctx) {
  try {
    return (await ctx.tailnet.self()).epoch;
  } catch {
    return 0;
  }
}

export class Pulse extends DurableObject {
  async fetch(req) {
    const u = new URL(req.url);
    if (u.pathname.endsWith("/start")) {
      await this.ctx.storage.put("url", u.searchParams.get("url"));
      await this.ctx.storage.setAlarm(Date.now() + 100);
      return Response.json({ ok: true, epoch: await epochOf(this.ctx) });
    }
    return Response.json({ url: (await this.ctx.storage.get("url")) ?? null });
  }
  async alarm() {
    const url = await this.ctx.storage.get("url");
    if (!url) return;
    await this.ctx.storage.setAlarm(Date.now() + 250);
    const epoch = await epochOf(this.ctx);
    try {
      await fetch(url + "?epoch=" + epoch + "&t=" + Date.now(), { method: "POST" });
    } catch {}
  }
}

export class Reg extends DurableObject {
  async fetch(req) {
    const u = new URL(req.url);
    if (u.pathname.endsWith("/clock")) return Response.json({ now: Date.now() });
    const epoch = await epochOf(this.ctx);
    let v =(await this.ctx.storage.get("v")) ?? 0;
    if (u.pathname.endsWith("/incr")) {
      v += 1;
      await this.ctx.storage.put("v", v);
    }
    return Response.json({ v, epoch });
  }
}

let d1Ready = false;
async function d1(env) {
  if (!d1Ready) {
    await env.DB.prepare("CREATE TABLE IF NOT EXISTS ops (id TEXT PRIMARY KEY)").run();
    d1Ready = true;
  }
  return env.DB;
}

export default {
  async fetch(req, env, ctx) {
    const u = new URL(req.url);
    const p = u.pathname;
    try {
      if (p === "/send") {
        await env.JOBS.send({ k: u.searchParams.get("k") });
        return Response.json({ sent: 1 });
      }
      if (p.startsWith("/reg/")) return env.REG.getByName("r").fetch(req);
      if (p.startsWith("/pulse/")) return env.PULSE.getByName("p").fetch(req);
      if (p === "/d1/add") {
        const db = await d1(env);
        await db.prepare("INSERT OR IGNORE INTO ops (id) VALUES (?)").bind(u.searchParams.get("id")).run();
        return Response.json({ id: u.searchParams.get("id") });
      }
      if (p === "/kv/put") {
        const ttl = Number(u.searchParams.get("ttl") || 0);
        await env.KV.put(u.searchParams.get("k"), u.searchParams.get("v"), ttl ? { expirationTtl: ttl } : undefined);
        return Response.json({ epoch: await epochOf(ctx) });
      }
      if (p === "/kv/get") {
        const v = await env.KV.get(u.searchParams.get("k"));
        return Response.json({ v, epoch: await epochOf(ctx) });
      }
      if (p === "/clock") {
        const obj = await (await env.REG.getByName("r").fetch("http://dp/reg/clock")).json();
        return Response.json({ now: Date.now(), obj: obj.now });
      }
      if (p === "/d1/all") {
        const db = await d1(env);
        const { results } = await db.prepare("SELECT id FROM ops").all();
        return Response.json({ ids: results.map((r) => r.id) });
      }
    } catch (e) {
      return Response.json({ error: String(e && e.message) }, { status: 500 });
    }
    return lockWorker.fetch(req, env, ctx);
  },
  async queue(batch, env) {
    const recs = batch.messages.map((m) => ({ q: batch.queue, k: m.body.k, id: m.id, attempts: m.attempts }));
    const r = await fetch(env.REC + "/q", { method: "POST", body: JSON.stringify(recs) });
    if (!r.ok) throw new Error("recorder answered " + r.status);
    if (batch.queue !== "jobs") return;
    // Settle each message explicitly, then throw if the batch held poison:
    // only the poison (never acked) is retried and, in the end, dead-lettered.
    let poison = false;
    for (const m of batch.messages) {
      if (m.body.k.endsWith("-x")) poison = true;
      else if (m.body.k.endsWith("-r") && m.attempts === 1) m.retry({ delaySeconds: 1 });
      else m.ack();
    }
    if (poison) throw new Error("poison in batch");
  },
};
