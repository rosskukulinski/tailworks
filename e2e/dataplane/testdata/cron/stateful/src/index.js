// e2e/dataplane cron loop, stateful workload (#419): the cron triggers of
// ../../cron.js plus Durable Object alarms, all reporting to the recorder.
//
//   GET /arm?cls=alm|falm&name=<n>  → a new alarm chain on that object:
//                                     storage {chain, seq: 1}, setAlarm(+1 s)
//   GET /sch/start?name=<n>         → the Sch agent: scheduleEvery(2, "tick")
//
// A chain's alarm() reports `fire` for its seq, then sets the next alarm
// (+2 s) and reports `set` for seq+1:
//
//   POST /alarm?cls&name&chain&seq&kind=fire|set&delay&epoch
//
// fire is reported before the storage writes, and alarm() throws if the
// report didn't get through (workerd then retries it), so an alarm() that
// ran is never missing from the record. Alm is a plain DO (workerd's
// alarms); FAlm and Sch are facets, whose alarms tailworks delegates to the
// Host DO.
import { DurableObject } from "cloudflare:workers";
import { Agent, getAgentByName } from "agents";
import { scheduled, epochOf, report } from "../../cron.js";

class Chain extends DurableObject {
  cls() {
    return "";
  }
  async rep(kind, name, chain, seq, delay) {
    const epoch = await epochOf(this.ctx);
    return report(this.env.REC, "/alarm", { cls: this.cls(), name, chain, seq, kind, delay, epoch });
  }
  async fetch(req) {
    const name = new URL(req.url).searchParams.get("name");
    const chain = crypto.randomUUID().slice(0, 12);
    await this.ctx.storage.put({ chain, seq: 1, name });
    await this.ctx.storage.setAlarm(Date.now() + 1000);
    await this.rep("set", name, chain, 1, 1000);
    return Response.json({ chain, epoch: await epochOf(this.ctx) });
  }
  async alarm() {
    const st = await this.ctx.storage.get(["chain", "seq", "name"]);
    const chain = st.get("chain");
    if (!chain) return;
    const seq = st.get("seq");
    const name = st.get("name");
    // Unrecorded, the fire would look lost: throw, and workerd retries alarm().
    if (!(await this.rep("fire", name, chain, seq, 0))) throw new Error("recorder unreachable");
    await this.ctx.storage.put("seq", seq + 1);
    await this.ctx.storage.setAlarm(Date.now() + 2000);
    await this.rep("set", name, chain, seq + 1, 2000);
  }
}

export class Alm extends Chain {
  cls() {
    return "alm";
  }
}

export class FAlm extends Chain {
  cls() {
    return "falm";
  }
}

// An Agents SDK agent: scheduleEvery() is idempotent, so the client may
// start it again on every new epoch.
export class Sch extends Agent {
  async onRequest() {
    await this.scheduleEvery(2, "tick");
    return Response.json({ ok: true, epoch: await epochOf(this.ctx) });
  }
  async tick() {
    const n = ((await this.ctx.storage.get("n")) ?? 0) + 1;
    await this.ctx.storage.put("n", n);
    await report(this.env.REC, "/alarm", { cls: "sch", name: this.name, chain: "every", seq: n, kind: "fire", delay: 0, epoch: await epochOf(this.ctx) });
  }
}

export default {
  async fetch(req, env) {
    const u = new URL(req.url);
    const name = u.searchParams.get("name") ?? "x";
    try {
      if (u.pathname === "/arm") {
        const ns = u.searchParams.get("cls") === "falm" ? env.FALM : env.ALM;
        return await ns.getByName(name).fetch(req);
      }
      if (u.pathname === "/sch/start") {
        return await (await getAgentByName(env.SCH, name)).fetch(req);
      }
    } catch (e) {
      return Response.json({ error: String(e && e.message) }, { status: 500 });
    }
    return Response.json({ wl: "cronobj" });
  },
  async scheduled(controller, env, ctx) {
    await scheduled("cronobj", controller, env, ctx);
  },
};
