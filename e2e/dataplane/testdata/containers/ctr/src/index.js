// e2e/dataplane containers loop (issue #423): every Box object owns one
// container (ADR-032). GET /c/<object>/<op>:
//   up    start the container unless it runs (an entrypoint that ignores
//         SIGTERM, so destroy() has to hit the grace cap), then read its
//         boot_id and 16 bytes of urandom; report them to the recorder from
//         the object's own node (facet egress) and answer them
//   down  destroy() it; answer how long that took
// (The test kills retired objects through mgmt, as `tw kill` does.)
import { DurableObject } from "cloudflare:workers";

const dec = new TextDecoder();
const J = (v, status = 200) => Response.json(v, { status });
const ENTRY = ["sh", "-c", "trap '' TERM; while :; do sleep 1; done"];

async function sh(c, cmd) {
  const p = await c.exec(["sh", "-c", cmd]);
  const o = await p.output();
  if (o.exitCode !== 0) throw new Error(cmd + ": exit " + o.exitCode + " " + dec.decode(o.stderr));
  return dec.decode(o.stdout).trim();
}

export class Box extends DurableObject {
  async fetch(req) {
    const op = new URL(req.url).pathname.split("/").pop();
    const c = this.ctx.container;
    try {
      if (op === "up") {
        let started = false;
        if (!c.running) {
          c.start({ entrypoint: ENTRY });
          started = true;
        }
        const boot = await sh(c, "cat /proc/sys/kernel/random/boot_id");
        const rnd = await sh(c, "head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \\n'");
        const self = await this.ctx.tailnet.self();
        const rep = {
          obj: this.ctx.id.name,
          boot,
          rnd,
          started,
          epoch: self.epoch,
          magicDNS: self.magicDNS,
          // pending until the object's node is ready (the DO runs before it)
          status: self.status,
          hostname: self.hostname,
          objectID: self.objectID,
        };
        let recErr = null;
        try {
          const r = await fetch(this.env.REC + "/ctr", { method: "POST", body: JSON.stringify(rep) });
          if (r.status !== 204) recErr = "recorder " + r.status;
        } catch (e) {
          recErr = String(e?.message ?? e);
        }
        return J({ ...rep, recErr });
      }
      if (op === "down") {
        const t0 = Date.now();
        await c.destroy();
        return J({ ms: Date.now() - t0, running: c.running });
      }
      return J({ error: "unknown op " + op }, 404);
    } catch (e) {
      return J({ error: String(e?.message ?? e), code: e?.code ?? null }, 500);
    }
  }
}

export default {
  fetch(req, env) {
    const [, kind, name] = new URL(req.url).pathname.split("/");
    if (kind !== "c" || !name) return J({ error: "GET /c/<object>/<op>" }, 404);
    return env.BOX.getByName(name).fetch(req);
  },
};
