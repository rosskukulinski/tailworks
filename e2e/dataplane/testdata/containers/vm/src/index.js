// e2e/dataplane containers loop (issue #423): what a restored microVM copy
// must not share with another (ADR-030 "Uniqueness"). GET /id answers this
// workerd's first random draws (taken once, on the first request after the
// load) and reports them to the recorder over the workload's own egress.
let first = null;

export default {
  async fetch(req, env) {
    const u = new URL(req.url);
    if (u.pathname !== "/id") return new Response("not found", { status: 404 });
    if (first === null) {
      const b = crypto.getRandomValues(new Uint8Array(16));
      first = {
        wl: u.hostname,
        uuid: crypto.randomUUID(),
        mr: Math.random(),
        rnd: [...b].map((x) => x.toString(16).padStart(2, "0")).join(""),
        drawnAt: Date.now(),
        recErr: null,
      };
      try {
        const r = await fetch(env.REC + "/vm", { method: "POST", body: JSON.stringify(first) });
        if (r.status !== 204) first.recErr = "recorder " + r.status;
      } catch (e) {
        first.recErr = String(e?.message ?? e);
      }
    }
    return Response.json({ ...first, now: Date.now() });
  },
};
