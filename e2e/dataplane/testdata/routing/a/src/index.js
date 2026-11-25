// e2e/dataplane routing loop (#422), Worker A. Every answer carries A's
// (host, epoch, ver); binding answers carry B's too, and the caller B saw.
//
//   /pub/hello, /t/hello   → {a, req}
//   /pub/bind, /bind/fetch → {a, b, ms}: env.B.fetch() (ms: how long it took)
//   /bind/rpc              → {a, b, ms}: env.B.ident() (RPC)
//   /gate/hit?id=          → {a}; reports the hit to the recorder first, so
//                            a request that reached workerd is on record
//                            even if its answer is lost
import { DurableObject } from "cloudflare:workers";
import { getRequester } from "@tailworks/tailnet";

export class Pin extends DurableObject {
  async fetch() {
    return Response.json({ ok: true });
  }
}

async function ident(ctx, env) {
  let s = {};
  try {
    s = await ctx.tailnet.self();
  } catch (e) {
    s = { error: String(e?.message ?? e) };
  }
  const hostname = s.hostname ?? "";
  return { hostname, host: hostname.split("-")[0], epoch: s.epoch ?? 0, ver: env.VER };
}

function requester(req) {
  try {
    const r = getRequester(req);
    return { kind: r.kind, hostname: r.hostname ?? null };
  } catch (e) {
    return { error: String(e?.message ?? e) };
  }
}

export default {
  async fetch(req, env, ctx) {
    const u = new URL(req.url);
    const p = u.pathname;
    const a = await ident(ctx, env);
    try {
      if (p === "/pub/hello" || p === "/t/hello") return Response.json({ a, req: requester(req) });
      const t0 = Date.now();
      if (p === "/pub/bind" || p === "/bind/fetch") {
        const r = await env.B.fetch("http://b/hello");
        const body = await r.text();
        const ms = Date.now() - t0;
        if (r.status !== 200) return Response.json({ a, ms, status: r.status, err: r.headers.get("tw-error"), body }, { status: 502 });
        return Response.json({ a, ms, b: JSON.parse(body), req: requester(req) });
      }
      if (p === "/bind/rpc") {
        try {
          const b = await env.B.ident();
          return Response.json({ a, ms: Date.now() - t0, b });
        } catch (e) {
          return Response.json({ a, ms: Date.now() - t0, error: String(e?.message ?? e), err: e?.code ?? null }, { status: 502 });
        }
      }
      if (p === "/gate/hit") {
        const q = new URLSearchParams({ id: u.searchParams.get("id") ?? "", ver: env.VER, epoch: String(a.epoch), host: a.host });
        await fetch(env.REC + "/gate?" + q, { method: "POST" }).catch(() => {});
        return Response.json({ a, req: requester(req) });
      }
    } catch (e) {
      return Response.json({ a, error: String(e?.message ?? e), name: e?.name }, { status: 500 });
    }
    return Response.json({ a, notFound: p }, { status: 404 });
  },
};
