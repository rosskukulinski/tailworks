// Test fixture for Queues: the Worker produces to "jobs" and consumes it.
// queue() records every delivery in a Durable Object it creates (through
// its guarded env, so with the batch's create ticket); GET /log returns
// them. A message {k, do} says what the handler does with it:
//   ack (default)  record it (implicitly acked)
//   retry-once     retry({delaySeconds: delay}) on attempt 1, then record
//   fail           retry() every time (max_retries 1: then the DLQ)
//   throw-once     throw on attempt 1 (the whole batch is retried)
//   hang-once      hang on attempt 1 (the test kills workerd)
//   slow-once      take 8 s the first time it arrives (the test stalls
//                  hostd's heartbeat); a redelivery, still attempt 1, is quick
//   ack-explicit   msg.ack(), then throw (the ack holds)
import { DurableObject } from "cloudflare:workers";

export class Log extends DurableObject {
  async add(recs) {
    const all = (await this.ctx.storage.get("recs")) ?? [];
    all.push(...recs);
    await this.ctx.storage.put("recs", all);
  }
  async list() {
    return (await this.ctx.storage.get("recs")) ?? [];
  }
}

export class Box extends DurableObject {
  async fetch(req) {
    const u = new URL(req.url);
    try {
      const r = await this.env.JOBS.send({ k: u.searchParams.get("k"), from: "facet" });
      return Response.json({ ok: true, backlog: r?.metadata?.metrics?.backlogCount ?? null });
    } catch (e) {
      return new Response(String(e?.message ?? e), { status: 500 });
    }
  }
}

// The slow-once messages this isolate has started (by message id).
const slowSeen = new Set();

function describe(body) {
  if (typeof body === "string") return "text:" + body;
  if (body instanceof ArrayBuffer) return "bytes:" + [...new Uint8Array(body)].join(",");
  if (body && body.d instanceof Date && body.m instanceof Map) return "v8:" + body.d.getTime() + ":" + body.m.get(1);
  return null;
}

export default {
  async fetch(req, env) {
    const u = new URL(req.url);
    const q = u.searchParams;
    try {
      switch (u.pathname) {
        case "/send": {
          const opts = q.has("delay") ? { delaySeconds: Number(q.get("delay")) } : {};
          const r = await env.JOBS.send({ k: q.get("k"), do: q.get("do") ?? "ack", delay: Number(q.get("rd") ?? 0) }, opts);
          return Response.json({ backlog: r?.metadata?.metrics?.backlogCount ?? null });
        }
        case "/batch": {
          const msgs = q.get("ks").split(",").map((k) => ({ body: { k, do: q.get("do") ?? "ack" } }));
          await env.JOBS.sendBatch(msgs);
          return Response.json({ ok: true });
        }
        case "/types":
          await env.JOBS.sendBatch([
            { body: "hello", contentType: "text" },
            { body: new Uint8Array([1, 2, 3]), contentType: "bytes" },
            { body: { d: new Date(5), m: new Map([[1, "two"]]) }, contentType: "v8" },
          ]);
          return Response.json({ ok: true });
        case "/slow":
          await env.SLOW.send({ k: q.get("k") });
          return Response.json({ ok: true });
        case "/big":
          try {
            await env.JOBS.send("x".repeat(130_000), { contentType: "text" });
            return Response.json({ sent: true });
          } catch (e) {
            return Response.json({ error: String(e?.message ?? e) });
          }
        case "/metrics": {
          const m = await env.JOBS.metrics();
          return Response.json({ backlogCount: m.backlogCount, backlogBytes: m.backlogBytes, oldest: m.oldestMessageTimestamp instanceof Date });
        }
        case "/log":
          return Response.json({ recs: await env.LOG.getByName("log").list() });
        case "/do/send":
          return env.BOX.getByName("b1").fetch(req);
        case "/env":
          return Response.json({ JOBS: typeof env.JOBS, OTHER: typeof env.OTHER, keys: Object.keys(env).filter((k) => k.startsWith("__")) });
      }
      return new Response("not found", { status: 404 });
    } catch (e) {
      return new Response(String(e?.message ?? e), { status: 500 });
    }
  },

  async queue(batch, env, ctx) {
    const recs = [];
    let throwIt = false;
    let hang = false;
    let slow = false;
    for (const m of batch.messages) {
      const b = m.body;
      const d = describe(b);
      const rec = { id: m.id, k: d ?? b?.k, from: b?.from, attempts: m.attempts, batch: batch.messages.length, queue: batch.queue, at: Date.now(), ts: m.timestamp?.getTime?.() ?? null };
      switch (d ? "ack" : (b?.do ?? "ack")) {
        case "retry-once":
          if (m.attempts === 1) {
            m.retry({ delaySeconds: b.delay });
            rec.retried = true;
          }
          break;
        case "fail":
          m.retry();
          rec.retried = true;
          break;
        case "throw-once":
          if (m.attempts === 1) throwIt = true;
          break;
        case "hang-once":
          if (m.attempts === 1) hang = true;
          break;
        case "slow-once":
          // Not "on attempt 1": the cancelled run is handed back uncounted,
          // so the redelivery is attempt 1 too. Slow again, it could be
          // cancelled again by any stale moment of a loaded host's lease,
          // over and over (#392).
          if (!slowSeen.has(m.id)) {
            slowSeen.add(m.id);
            slow = true;
          }
          break;
        case "ack-explicit":
          m.ack();
          throwIt = true;
          break;
      }
      recs.push(rec);
    }
    // A slow run records only when done, like a handler whose work came
    // before its log line: cancelled mid-way, it leaves no record.
    if (slow) await new Promise((r) => setTimeout(r, 8_000));
    await env.LOG.getByName("log").add(recs);
    if (hang) await new Promise((r) => setTimeout(r, 120_000));
    if (throwIt) throw new Error("boom from queue()");
  },
};
