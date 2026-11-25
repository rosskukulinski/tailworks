// The e2e/dataplane deploys workload (issue #421, LOOP.md "Deploys").
//
//   GET /info → 200 {epoch, inst, build, sec, wbuild, wsec}
//     epoch:  the lease epoch the Info Durable Object runs at (tailnet.self())
//     inst:   a random ID of this copy of the Durable Object
//     build:  the bundle's BUILD var, as the Durable Object sees it
//     sec:    per secret name, the first 16 hex digits of the SHA-256 of its
//             value as the Durable Object sees it ("-" when unset)
//     wbuild, wsec: the same, as the Worker sees them
//
// Only digests leave the Worker: the test never logs or records a secret
// value, and it checks that no process log does either.
import { DurableObject } from "cloudflare:workers";

const NAMES = ["SEC_A", "SEC_B", "SEC_C", "SEC_P"];

async function digest(v) {
  if (typeof v !== "string") return "-";
  const h = new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(v)));
  return [...h.slice(0, 8)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

async function secrets(env) {
  const out = {};
  for (const n of NAMES) out[n] = await digest(env[n]);
  return out;
}

export class Info extends DurableObject {
  async fetch() {
    // inst names this copy of the object (a new workerd, a new deploy):
    // the epoch is the workload's current one, so during a redeploy on the
    // same host the previous copy answers at the new epoch.
    this.inst ??= crypto.randomUUID();
    let epoch = 0;
    try {
      epoch = (await this.ctx.tailnet.self()).epoch;
    } catch {}
    return Response.json({ epoch, inst: this.inst, build: this.env.BUILD, sec: await secrets(this.env) });
  }
}

export default {
  async fetch(req, env) {
    const u = new URL(req.url);
    if (u.pathname !== "/info") return new Response("not found", { status: 404 });
    try {
      const r = await env.INFO.getByName("i").fetch(req);
      const txt = await r.text();
      let j = null;
      try {
        j = JSON.parse(txt);
      } catch {}
      if (!r.ok || !j || !j.build) {
        const err = "the Durable Object answered " + r.status + ": " + txt.slice(0, 300);
        return Response.json({ error: err, wbuild: env.BUILD }, { status: 502 });
      }
      j.wbuild = env.BUILD;
      j.wsec = await secrets(env);
      return Response.json(j);
    } catch (e) {
      return Response.json({ error: String(e && e.message) }, { status: 500 });
    }
  },
};
