// e2e/dataplane routing loop (#422), Worker B: answers fetch() and the
// ident() RPC with its own (host, epoch, ver) and the caller it saw.
import { DurableObject, WorkerEntrypoint } from "cloudflare:workers";
import { getCaller } from "@tailworks/tailnet";

export class Pin extends DurableObject {
  async fetch() {
    return Response.json({ ok: true });
  }
}

function caller(x) {
  try {
    return getCaller(x).hostname;
  } catch (e) {
    return "error:" + (e?.code ?? String(e));
  }
}

async function ident(ctx, env, x) {
  let s = {};
  try {
    s = await ctx.tailnet.self();
  } catch (e) {
    s = { error: String(e?.message ?? e) };
  }
  const hostname = s.hostname ?? "";
  return { hostname, host: hostname.split("-")[0], epoch: s.epoch ?? 0, ver: env.VER, caller: caller(x) };
}

export default class B extends WorkerEntrypoint {
  async fetch(req) {
    return Response.json(await ident(this.ctx, this.env, req));
  }
  async ident() {
    return ident(this.ctx, this.env, this.ctx);
  }
}
