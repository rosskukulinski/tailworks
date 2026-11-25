// e2e/vm cron fixture: scheduled() records each run in a Durable Object;
// GET /runs returns them.
import { DurableObject } from "cloudflare:workers";

export class Log extends DurableObject {
  async add(run) {
    const runs = (await this.ctx.storage.get("runs")) ?? [];
    runs.push(run);
    await this.ctx.storage.put("runs", runs);
  }
  async list() {
    return (await this.ctx.storage.get("runs")) ?? [];
  }
}

export default {
  async fetch(req, env) {
    if (new URL(req.url).pathname === "/runs") return Response.json({ runs: await env.LOG.getByName("log").list() });
    return Response.json({ kind: "vmcron" });
  },
  async scheduled(controller, env, ctx) {
    await env.LOG.getByName("log").add({ cron: controller.cron, scheduledTime: controller.scheduledTime, hasTailnet: !!ctx.tailnet, at: Date.now() });
  },
};
