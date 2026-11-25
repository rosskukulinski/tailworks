// Test fixture for worker secrets: GET / echoes env from the worker, GET
// /do from a facet Durable Object (its env comes from tw-config.json).
import { DurableObject } from "cloudflare:workers";

const view = (env) => ({ apiKey: env.API_KEY, type: typeof env.API_KEY, greeting: env.GREETING, multi: env.MULTI ?? null });

export class Box extends DurableObject {
  async fetch() {
    return Response.json(view(this.env));
  }
}

export default {
  async fetch(req, env) {
    const url = new URL(req.url);
    if (url.pathname === "/do") return env.BOX.getByName("b1").fetch(new Request("http://box/"));
    return Response.json(view(env));
  },
};
