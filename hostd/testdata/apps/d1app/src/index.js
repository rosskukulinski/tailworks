import { DurableObject } from "cloudflare:workers";

// The test drives env.DB over HTTP: POST a JSON body, get JSON back. A
// parameter {"$u8": [..]} is bound as a Uint8Array, {"$big": "5"} as a
// BigInt. Errors come back as {"error": message}.
function arg(v) {
  if (v && typeof v === "object" && Array.isArray(v.$u8)) return new Uint8Array(v.$u8);
  if (v && typeof v === "object" && typeof v.$big === "string") return BigInt(v.$big);
  return v;
}
function stmt(db, q) {
  const s = db.prepare(q.sql);
  return q.params ? s.bind(...q.params.map(arg)) : s;
}
async function call(db, q) {
  const s = stmt(db, q);
  switch (q.method ?? "all") {
    case "first":
      return q.col !== undefined ? s.first(q.col) : s.first();
    case "run":
      return s.run();
    case "raw":
      return s.raw(q.columnNames ? { columnNames: true } : undefined);
    default:
      return s.all();
  }
}

async function serve(env, u, body) {
  const db = env.DB;
  switch (u.pathname.replace(/^\/do/, "") || "/") {
    case "/info":
      return { type: typeof db, methods: ["prepare", "batch", "exec", "withSession", "dump"].filter((m) => typeof db?.[m] === "function") };
    case "/q":
      return { r: await call(db, body) };
    case "/batch":
      return { r: await db.batch(body.map((q) => stmt(db, q))) };
    case "/exec":
      return { r: await db.exec(body.sql) };
    case "/dump":
      return { r: await db.dump() };
    case "/session": {
      // body: {constraint, steps: [q…]}: run the steps in one session.
      const s = db.withSession(body.constraint);
      const out = [];
      for (const q of body.steps) {
        out.push({ r: await call(s, q), bookmark: s.getBookmark() });
      }
      return { steps: out };
    }
  }
  return { error: "not found" };
}

async function handle(env, req) {
  const u = new URL(req.url);
  try {
    const body = req.method === "POST" ? await req.json() : null;
    return Response.json(await serve(env, u, body));
  } catch (e) {
    return Response.json({ error: String(e?.message ?? e) });
  }
}

export class Box extends DurableObject {
  fetch(req) {
    return handle(this.env, req);
  }
}

export default {
  fetch(req, env) {
    if (new URL(req.url).pathname.startsWith("/do/")) return env.BOX.getByName("b1").fetch(req);
    return handle(env, req);
  },
};
