// tailworks Queues (TRUSTED; runs in the platform worker, never in a tenant
// isolate). ADR-039.
//
// Producer: a tenant's env.<Binding> is workerd's own `queue` binding,
// targeting QueueProducer here with props {queue} that tenant code can't
// change. workerd turns send()/sendBatch()/metrics() into HTTP requests
// (/message with X-Msg-Fmt and X-Msg-Delay-Secs, /batch with base64
// bodies, /metrics); QueueProducer forwards them to hostd's local API,
// which checks the deployment binds the queue and sends to mgmt as the
// workload's lease holder. Facets get QueueFacetProxy, which forwards to a
// native binding of the same queue held here (__TW_QUEUE_<Binding>),
// because native bindings can't enter a Dynamic Worker's env.
//
// Consumer: hostd posts a claimed batch over the private ingress as
// cf.tw {kind: "queue"}; deliverQueue decodes the bodies and runs the
// tenant's queue() handler with workerd's own MessageBatch
// (env.TENANT.queue, service_binding_extra_handlers). The batch's first
// message carries the delivery's create ticket; the tenant runtime strips
// it (tw-rt queueBatch).
import { WorkerEntrypoint } from "cloudflare:workers";
import CFG from "tw-config.json";

const TICKET_ID = "__tw_ticket";
const td = new TextDecoder();

function b64enc(u8) {
  if (typeof u8.toBase64 === "function") return u8.toBase64();
  let s = "";
  for (let i = 0; i < u8.length; i += 0x8000) s += String.fromCharCode.apply(null, u8.subarray(i, i + 0x8000));
  return btoa(s);
}
function b64dec(s) {
  if (typeof Uint8Array.fromBase64 === "function") return Uint8Array.fromBase64(s);
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

async function hostdQueue(env, op, body) {
  let r;
  try {
    r = await env.HOSTD.fetch("http://hostd/v1/queue/" + op, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body),
    });
  } catch (e) {
    throw new Error("[queue_unavailable] hostd: " + String(e?.message ?? e));
  }
  let j = null;
  try {
    j = await r.json();
  } catch {
    /* not JSON */
  }
  if (!r.ok) throw Object.assign(new Error("[" + (j?.code ?? "queue_unavailable") + "] " + (j?.message ?? "queue " + op + " answered " + r.status)), { status: r.status });
  return j;
}

// queueError answers the binding with an error workerd surfaces to the
// caller as `Error: <cause> (<code>)`; the code is the HTTP status.
function queueError(e) {
  const status = Number(e?.status) || 503;
  const cause = String(e?.message ?? e).replace(/[\r\n]+/g, " ").slice(0, 1024);
  return new Response(cause, { status, headers: { "cf-queues-error-code": String(status), "cf-queues-error-cause": cause } });
}

function metricsOf(r) {
  return { backlogCount: Number(r?.backlog ?? 0), backlogBytes: Number(r?.bytes ?? 0), oldestMessageTimestamp: Number(r?.oldestAt ?? 0) };
}

function delayOf(v) {
  if (v == null || v === "") return undefined;
  const n = Number(v);
  return Number.isFinite(n) ? Math.trunc(n) : undefined;
}

// QueueProducer backs a tenant's queue binding (props {queue, binding}).
export class QueueProducer extends WorkerEntrypoint {
  async fetch(req) {
    const { queue, binding } = this.ctx.props ?? {};
    const p = new URL(req.url).pathname;
    try {
      if (p === "/message") {
        const body = new Uint8Array(await req.arrayBuffer());
        const m = { body: b64enc(body), contentType: req.headers.get("x-msg-fmt") || "v8" };
        const d = delayOf(req.headers.get("x-msg-delay-secs"));
        if (d !== undefined) m.delaySeconds = d;
        const r = await hostdQueue(this.env, "send", { queue, binding, messages: [m] });
        return Response.json({ metadata: { metrics: metricsOf(r) } });
      }
      if (p === "/batch") {
        const j = await req.json();
        const def = delayOf(req.headers.get("x-msg-delay-secs"));
        const messages = (j?.messages ?? []).map((m) => {
          const o = { body: String(m.body ?? ""), contentType: m.contentType || "v8" };
          const d = delayOf(m.delaySecs) ?? def;
          if (d !== undefined) o.delaySeconds = d;
          return o;
        });
        const r = await hostdQueue(this.env, "send", { queue, binding, messages });
        return Response.json({ metadata: { metrics: metricsOf(r) } });
      }
      if (p === "/metrics") {
        return Response.json(metricsOf(await hostdQueue(this.env, "metrics", { queue, binding })));
      }
      return new Response("unknown queue operation " + p, { status: 404 });
    } catch (e) {
      return queueError(e);
    }
  }
}

// QueueFacetProxy is a facet's env.<Binding> (props {binding}).
export class QueueFacetProxy extends WorkerEntrypoint {
  #q() {
    return this.env["__TW_QUEUE_" + this.ctx.props?.binding];
  }
  send(body, opts) {
    return this.#q().send(body, opts);
  }
  sendBatch(msgs, opts) {
    return this.#q().sendBatch(msgs, opts);
  }
  metrics() {
    return this.#q().metrics();
  }
}

function decode(m) {
  const bytes = b64dec(m.body ?? "");
  const out = { id: String(m.id), timestamp: new Date(Number(m.timestamp)), attempts: Number(m.attempts) || 1 };
  switch (m.contentType) {
    case "v8":
      out.serializedBody = bytes;
      break;
    case "text":
      out.body = td.decode(bytes);
      break;
    case "bytes":
      out.body = bytes.buffer.slice(bytes.byteOffset, bytes.byteOffset + bytes.byteLength);
      break;
    default:
      out.body = JSON.parse(td.decode(bytes));
  }
  return out;
}

const json = (status, body) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json", "cache-control": "no-store" } });

// deliverQueue runs one batch (cf.tw.kind "queue", set only by hostd) and
// answers {outcome, ackAll, retryBatch, explicitAcks, retryMessages, error}.
export async function deliverQueue(env, t, req) {
  const name = t.queue;
  if (typeof name !== "string" || !(CFG.queues?.consumers ?? []).includes(name)) {
    return json(400, { code: "bad_request", message: "this deployment has no consumer of queue " + String(name) });
  }
  let msgs;
  try {
    const b = await req.json();
    msgs = (b?.messages ?? []).map(decode);
  } catch (e) {
    return json(400, { code: "bad_request", message: "bad queue batch: " + String(e?.message ?? e) });
  }
  msgs.unshift({ id: TICKET_ID, timestamp: new Date(), body: typeof t.ticket === "string" ? t.ticket : "", attempts: 1 });
  try {
    const r = await env.TENANT.queue(name, msgs);
    return json(200, {
      outcome: String(r?.outcome ?? "exception"),
      ackAll: !!r?.ackAll,
      retryBatch: { retry: !!r?.retryBatch?.retry, delaySeconds: r?.retryBatch?.delaySeconds },
      explicitAcks: (r?.explicitAcks ?? []).filter((id) => id !== TICKET_ID),
      retryMessages: (r?.retryMessages ?? []).filter((m) => m.msgId !== TICKET_ID),
    });
  } catch (e) {
    return json(200, { outcome: "exception", error: String(e?.message ?? e).slice(0, 512) });
  }
}
