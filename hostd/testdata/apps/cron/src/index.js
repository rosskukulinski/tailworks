// Test fixture for cron triggers: scheduled() records each run in a
// Durable Object; GET /runs returns them.
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
  async release(t) {
    await this.ctx.storage.put("release:" + t, true);
  }
  async released(t) {
    return (await this.ctx.storage.get("release:" + t)) === true;
  }
}

export default {
  async fetch(req, env) {
    const url = new URL(req.url);
    if (url.pathname === "/runs") return Response.json(await env.LOG.getByName("log").list());
    if (url.pathname === "/release" && req.method === "POST") {
      // Lets the "1 0 4 1 *" run with this scheduledTime finish its waitUntil work.
      await env.LOG.getByName("log").release(Number(url.searchParams.get("t")));
      return Response.json({ ok: true });
    }
    return Response.json({ kind: "cron-fixture" });
  },
  async scheduled(controller, env, ctx) {
    const run = { cron: controller.cron, scheduledTime: controller.scheduledTime, type: controller.type ?? null, hasTailnet: !!ctx.tailnet, at: Date.now() };
    if (controller.cron === "0 0 1 1 *") throw new Error("boom from scheduled");
    if (controller.cron === "0 0 2 1 *") {
      await env.LOG.getByName("log").add({ ...run, phase: "start" });
      await new Promise((r) => setTimeout(r, Number(env.SLEEP_MS ?? 1500)));
      run.phase = "end";
    }
    if (controller.cron === "1 0 4 1 *") {
      // Returns at once but leaves work running in ctx.waitUntil(): the
      // run lasts until that work settles (cron overlap and timeout, H17).
      // The work holds until the test releases this run (POST /release),
      // so the test, not a sleep, decides when it ends.
      const log = env.LOG.getByName("log");
      await log.add({ ...run, phase: "start" });
      ctx.waitUntil(
        (async () => {
          while (!(await log.released(controller.scheduledTime))) await new Promise((r) => setTimeout(r, 50));
          await log.add({ ...run, phase: "end" });
        })(),
      );
      return;
    }
    if (controller.cron === "0 0 3 1 *") {
      try {
        const r = await fetch(env.ECHO_URL ?? "http://echo/from-cron");
        run.egress = { status: r.status, body: await r.text() };
      } catch (e) {
        run.egress = { error: String(e) };
      }
    }
    await env.LOG.getByName("log").add(run);
  },
};
