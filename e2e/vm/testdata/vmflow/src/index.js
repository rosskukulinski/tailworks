// e2e/vm Workflows fixture: Flow does a step, a step that fails once and
// is retried, and a sleep (?sleep=ms, default 10), then returns. The Worker
// and a facet-mode object drive it through env.FLOW; /do/... goes to the
// facet.
import { WorkflowEntrypoint, DurableObject } from "cloudflare:workers";

let tries = 0;

export class Flow extends WorkflowEntrypoint {
  async run(event, step) {
    const a = await step.do("first", async () => ({ n: event.payload.n }));
    const b = await step.do("flaky", { retries: { limit: 3, delay: 10, backoff: "constant" } }, async () => {
      tries++;
      if (tries % 2 === 1) throw new Error("try again");
      return a.n + 1;
    });
    await step.sleep("nap", event.payload.sleep);
    const c = await step.do("after-nap", async () => b * 10);
    return { b, c };
  }
}

async function drive(env, url) {
  const u = new URL(url);
  const id = u.searchParams.get("id");
  try {
    switch (u.pathname.replace(/^\/do/, "")) {
      case "/create": {
        const i = await env.FLOW.create({ id, params: { n: Number(u.searchParams.get("n") ?? 1), sleep: Number(u.searchParams.get("sleep") ?? 10) } });
        return { id: i.id };
      }
      case "/status":
        return await (await env.FLOW.get(id)).status();
    }
  } catch (e) {
    return { error: String(e?.message ?? e) };
  }
  return { error: "no route" };
}

export class Facet extends DurableObject {
  async fetch(req) {
    return Response.json(await drive(this.env, req.url));
  }
}

export default {
  async fetch(req, env) {
    if (new URL(req.url).pathname.startsWith("/do/")) return env.F.getByName("f1").fetch(req);
    return Response.json(await drive(env, req.url));
  },
};
