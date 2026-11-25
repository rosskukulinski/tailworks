import { DurableObject } from "cloudflare:workers";

async function serve(env, u) {
  const q = u.searchParams;
  const p = u.pathname.replace(/^\/do/, "") || "/";
  switch (p) {
    case "/put":
      return Response.json({ seq: await env.P.put(q.get("k"), q.get("v")) });
    case "/del":
      return Response.json({ seq: await env.P.del(q.get("k")) });
    case "/delif":
      return Response.json({ seq: await env.P.delIf(q.get("k")) });
    case "/get":
      return Response.json({ v: await env.P.get(q.get("k")) });
    case "/all":
      return Response.json(await env.P.all());
  }
  return new Response("not found", { status: 404 });
}

export class Box extends DurableObject {
  async fetch(req) {
    try {
      return await serve(this.env, new URL(req.url));
    } catch (e) {
      return new Response(String(e?.message ?? e), { status: 500 });
    }
  }
}

export default {
  async fetch(req, env) {
    const u = new URL(req.url);
    try {
      if (u.pathname.startsWith("/do/")) return env.BOX.getByName("b1").fetch(req);
      return await serve(env, u);
    } catch (e) {
      return new Response(String(e?.message ?? e), { status: 500 });
    }
  },
};
