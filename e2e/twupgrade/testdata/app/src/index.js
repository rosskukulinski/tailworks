// GET /: what the workload sees in its KV namespace and D1 database.
export default {
  async fetch(req, env) {
    const greeting = await env.CACHE.get("greeting");
    let notes = [];
    try {
      notes = (await env.DB.prepare("SELECT body FROM notes ORDER BY id").all()).results.map((r) => r.body);
    } catch (e) {
      notes = [String(e?.message ?? e)];
    }
    return Response.json({ greeting, notes });
  },
};
