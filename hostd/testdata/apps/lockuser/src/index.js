// Test fixture for the lock template's client (docs/locks.md).
import { lockClient } from "../../../../../js/packages/create-tailworks/templates/lock/src/client";

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

export default {
  async fetch(req, env) {
    const u = new URL(req.url);
    const q = (k, d) => u.searchParams.get(k) ?? d;
    const name = q("name", "job:test");
    const locks = lockClient({ url: env.LOCKS_URL });
    const write = async (token, what) => {
      const r = await fetch(env.STORE_URL + "/write?resource=" + encodeURIComponent(name), { method: "POST", headers: { "fencing-token": String(token) }, body: what });
      return { status: r.status, body: await r.json() };
    };
    try {
      switch (u.pathname) {
        case "/with": {
          // Hold the lock for `hold` ms (longer than the lease, so it must
          // be renewed), writing at the start and at the end.
          const writes = [];
          let renewedTo = 0;
          const out = await locks.withLock(
            name,
            async (l) => {
              writes.push(await write(l.token, "start"));
              const first = l.lease().deadline;
              await sleep(Number(q("hold", "0")));
              renewedTo = l.lease().deadline - first;
              writes.push(await write(l.token, "end"));
              return { token: l.token, aborted: l.signal.aborted };
            },
            { ttlMs: Number(q("ttl", "1000")), waitMs: Number(q("wait", "0")) },
          );
          return Response.json({ ok: true, ...out, renewedByMs: renewedTo, writes });
        }
        case "/acquire": {
          const l = await locks.tryAcquire(name, { ttlMs: Number(q("ttl", "1000")) });
          return Response.json({ ok: !!l, lease: l });
        }
        case "/renew": {
          const l = await locks.renew({ name, token: Number(q("token")), ttlMs: Number(q("ttl", "1000")) });
          return Response.json({ ok: true, lease: l });
        }
        case "/write":
          return Response.json(await write(Number(q("token")), "late"));
        case "/status":
          return Response.json(await locks.status(name));
      }
      return Response.json({ error: "not_found" }, { status: 404 });
    } catch (e) {
      return Response.json({ ok: false, code: e.code ?? "error", message: String(e.message ?? e), body: e.body ?? null }, { status: 299 });
    }
  },
};
