// Drives env.<binding>.<op>(...args) from a JSON request, in the worker or
// in a facet (/do/op), and serializes what KV returns (Maps, ArrayBuffers,
// streams) so the Go test can compare it.
import { DurableObject } from "cloudflare:workers";

async function ser(r) {
  if (r instanceof Map) {
    const o = {};
    for (const [k, v] of r) o[k] = await ser(v);
    return { map: o };
  }
  if (r instanceof ArrayBuffer) return { bytes: [...new Uint8Array(r)] };
  if (r instanceof ReadableStream) return { stream: await new Response(r).text() };
  if (r && typeof r === "object" && "value" in r && "metadata" in r) return { ...r, value: await ser(r.value) };
  return r === undefined ? { undefined: true } : r;
}

async function run(env, b) {
  const kv = env[b.binding ?? "KV"];
  const args = b.args ?? [];
  switch (b.op) {
    case "putBytes": // [key, [bytes...], opts]
      return kv.put(args[0], new Uint8Array(args[1]), args[2]);
    case "putSize": // [key, n]: n bytes of "x"
      return kv.put(args[0], "x".repeat(args[1]));
    case "putStream": // [key, text]
      return kv.put(args[0], new Response(args[1]).body);
    case "fill": // [prefix, n]
      for (let i = 0; i < args[1]; i++) await kv.put(args[0] + String(i).padStart(3, "0"), String(i));
      return args[1];
    case "listAll": { // [opts]: follow cursors
      const names = [];
      const pages = [];
      let cursor;
      for (;;) {
        const r = await kv.list({ ...args[0], cursor });
        names.push(...r.keys.map((k) => k.name));
        pages.push(r.keys.length);
        if (r.list_complete) break;
        cursor = r.cursor;
      }
      return { names, pages };
    }
  }
  return ser(await kv[b.op](...args));
}

async function serve(env, req) {
  try {
    return Response.json({ r: await run(env, await req.json()) });
  } catch (e) {
    return Response.json({ error: String(e?.message ?? e) }, { status: 500 });
  }
}

export class Box extends DurableObject {
  fetch(req) {
    return serve(this.env, req);
  }
}

export default {
  fetch(req, env) {
    const u = new URL(req.url);
    if (u.pathname === "/do/op") return env.BOX.getByName("b1").fetch(req);
    if (u.pathname === "/op") return serve(env, req);
    return new Response("not found", { status: 404 });
  },
};
