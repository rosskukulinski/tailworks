// pgprobe: postgres.js (its workerd build, over cloudflare:sockets
// connect()) from a Worker and from a facet-mode Durable Object to a
// Postgres on the tailnet (ADR-052; docs/recipes.md).
import postgres from "postgres";
import { DurableObject } from "cloudflare:workers";

async function query(url) {
  const sql = postgres(url, { max: 1, fetch_types: false });
  try {
    await sql`CREATE TABLE IF NOT EXISTS tw_hits (who text PRIMARY KEY, n int NOT NULL)`;
    const [r] = await sql`SELECT 1 + 1 AS two, current_user AS "user", inet_client_addr()::text AS client`;
    return r;
  } finally {
    await sql.end();
  }
}

export class Counter extends DurableObject {
  async fetch() {
    const sql = postgres(this.env.DATABASE_URL, { max: 1, fetch_types: false });
    try {
      await sql`CREATE TABLE IF NOT EXISTS tw_hits (who text PRIMARY KEY, n int NOT NULL)`;
      const [r] = await sql`INSERT INTO tw_hits VALUES (${this.ctx.id.name}, 1) ON CONFLICT (who) DO UPDATE SET n = tw_hits.n + 1 RETURNING n`;
      return Response.json({ n: r.n });
    } finally {
      await sql.end();
    }
  }
}

export default {
  async fetch(req, env) {
    try {
      const u = new URL(req.url);
      if (u.pathname.startsWith("/count/")) return env.COUNTER.getByName(u.pathname.slice(7)).fetch(req);
      return Response.json(await query(env.DATABASE_URL));
    } catch (e) {
      return Response.json({ error: String(e?.message ?? e) }, { status: 500 });
    }
  },
};
