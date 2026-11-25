// The lock template's Worker and Lock class, plus Pulse (see tailworks.jsonc).
import { DurableObject } from "cloudflare:workers";
import lockWorker from "../../../../../js/packages/create-tailworks/templates/lock/src/index";
export { Lock } from "../../../../../js/packages/create-tailworks/templates/lock/src/lock";

export class Pulse extends DurableObject {
  async fetch(req) {
    const u = new URL(req.url);
    if (u.pathname.endsWith("/start")) {
      await this.ctx.storage.put("url", u.searchParams.get("url"));
      await this.ctx.storage.setAlarm(Date.now() + 100);
      return Response.json({ ok: true });
    }
    return Response.json({ url: (await this.ctx.storage.get("url")) ?? null });
  }
  async alarm() {
    const url = await this.ctx.storage.get("url");
    if (!url) return;
    await this.ctx.storage.setAlarm(Date.now() + 250);
    let epoch = 0;
    try {
      epoch = (await this.ctx.tailnet.self()).epoch;
    } catch {}
    try {
      await fetch(url + "?epoch=" + epoch, { method: "POST" });
    } catch {}
  }
}

export default {
  async fetch(req, env, ctx) {
    if (new URL(req.url).pathname.startsWith("/pulse/")) return env.PULSE.getByName("p").fetch(req);
    return lockWorker.fetch(req, env, ctx);
  },
};
