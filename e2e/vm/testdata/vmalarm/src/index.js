// e2e/vm alarms fixture: GET /<CLOCK|PLAIN>/<name>/arm?ms=N sets an alarm N
// ms from now; alarm() counts its runs; GET /<CLOCK|PLAIN>/<name>/ returns
// the count and the pending alarm.
import { DurableObject } from "cloudflare:workers";

class Base extends DurableObject {
  async fetch(req) {
    const u = new URL(req.url);
    if (u.pathname.endsWith("/arm")) {
      const at = Date.now() + Number(u.searchParams.get("ms") ?? 100);
      await this.ctx.storage.setAlarm(at);
      return Response.json({ armed: at, pending: await this.ctx.storage.getAlarm() });
    }
    return Response.json({
      name: this.ctx.id.name,
      alarms: (await this.ctx.storage.get("alarms")) ?? 0,
      last: (await this.ctx.storage.get("last")) ?? null,
      pending: await this.ctx.storage.getAlarm(),
    });
  }
  async alarm() {
    await this.ctx.storage.put("alarms", ((await this.ctx.storage.get("alarms")) ?? 0) + 1);
    await this.ctx.storage.put("last", Date.now());
  }
}

export class Clock extends Base {}
export class PlainClock extends Base {}

export default {
  async fetch(req, env) {
    const [, ns, name] = new URL(req.url).pathname.split("/");
    if (!env[ns] || !name) return Response.json({ error: "want /CLOCK|PLAIN/<name>/..." }, { status: 404 });
    return env[ns].getByName(name).fetch(req);
  },
};
