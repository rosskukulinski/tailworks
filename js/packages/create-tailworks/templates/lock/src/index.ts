// lock: a lease-based lock and leader-election service. Each lock name is
// one Durable Object (src/lock.ts); its holder is the caller's tailnet
// identity. Clients use src/client.ts (acquire / renew / release / withLock).
//
//   POST /locks/<name>/acquire  {"ttlMs": 10000}  → 200 {token, expiresAt, holder, …} | 409 lock_held
//   POST /locks/<name>/renew    {"token": …}      → 200 {expiresAt, …} | 409 lock_lost
//   POST /locks/<name>/release  {"token": …}      → 200 | 409 lock_lost
//   GET  /locks/<name>                            → {held, holder, expiresAt, lastToken, …}
//
// docs/locks.md has the patterns (mutex, fencing tokens, leader election)
// and what tailworks guarantees when a host fails.
export { Lock } from "./lock";

// Env (the LOCK namespace, DEFAULT_TTL_MS, MAX_TTL_MS) is the global
// interface `tw types` writes to worker-configuration.d.ts. Run `tw types`
// again after changing wrangler.jsonc or tailworks.jsonc.

const ROUTE = /^\/locks\/([^/]+)(?:\/(acquire|renew|release))?\/?$/;
const NAME = /^[A-Za-z0-9._:@-]{1,200}$/;

export default {
  async fetch(req: Request, env: Env): Promise<Response> {
    const m = new URL(req.url).pathname.match(ROUTE);
    let name = "";
    try {
      name = m ? decodeURIComponent(m[1]) : "";
    } catch {}
    if (!NAME.test(name)) {
      return Response.json({ error: "not_found", detail: "POST /locks/<name>/acquire|renew|release, GET /locks/<name>; names: 1-200 of A-Z a-z 0-9 . _ : @ -" }, { status: 404 });
    }
    // Pass the original request on: its verified caller (request.cf) reaches
    // the object, which records it as the holder.
    return env.LOCK.getByName(name).fetch(req);
  },
} satisfies { fetch(req: Request, env: Env): Promise<Response> };
