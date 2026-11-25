// hostd wfcallback_test.go (#359): an object starts a Workflow, and the
// Workflow's step calls back into that object through its binding (what
// the Agents SDK's AgentWorkflow does with getAgentByName before run()).
// /start?class=&name= starts it; /pings?class=&name= reads what reached
// the object; /status?class=&name=&id= is the instance's status.
import { DurableObject, WorkflowEntrypoint } from "cloudflare:workers";

class Starter extends DurableObject {
  async start(binding) {
    const i = await this.env.CALLBACK.create({ params: { binding, name: this.ctx.id.name } });
    return i.id;
  }
  async ping(from) {
    const n = (this.ctx.storage.kv.get("pings") ?? 0) + 1;
    this.ctx.storage.kv.put("pings", n);
    this.ctx.storage.kv.put("from", from);
    return n;
  }
  async pings() {
    return { pings: this.ctx.storage.kv.get("pings") ?? 0, from: this.ctx.storage.kv.get("from") ?? null };
  }
}

export class Unlisted extends Starter {}
export class Internal extends Starter {}

export class Callback extends WorkflowEntrypoint {
  async run(event, step) {
    const { binding, name } = event.payload;
    return await step.do("callback", { retries: { limit: 0, delay: 10, backoff: "constant" } }, async () => {
      return await this.env[binding].getByName(name).ping(event.instanceId);
    });
  }
}

const BINDING = { Unlisted: "UNLISTED", Internal: "INTERNAL" };

export default {
  async fetch(req, env) {
    const u = new URL(req.url);
    const b = BINDING[u.searchParams.get("class")];
    const name = u.searchParams.get("name");
    if (!b || !name) return Response.json({ error: "class and name" }, { status: 400 });
    try {
      switch (u.pathname) {
        case "/start":
          return Response.json({ id: await env[b].getByName(name).start(b) });
        case "/pings":
          return Response.json(await env[b].getByName(name).pings());
        case "/status":
          return Response.json(await (await env.CALLBACK.get(u.searchParams.get("id"))).status());
      }
    } catch (e) {
      return Response.json({ error: String(e?.message ?? e) }, { status: 500 });
    }
    return Response.json({ error: "no route" }, { status: 404 });
  },
};
