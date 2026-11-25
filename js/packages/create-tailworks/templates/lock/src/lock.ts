// Lock: one Durable Object per lock name. A named object is single-threaded
// and runs on exactly one host (tailworks fences it with the workload's lease), so
// the object is the lock; the lease below only bounds how long a holder that
// stopped renewing keeps it. See docs/locks.md for the guarantees.
//
// Every acquire returns a fencing token. Tokens only ever increase, also
// across a move of this workload to another host (which starts the object
// with empty storage): token = max(last + 1, epoch × TOKEN_STRIDE + 1), where
// epoch is the workload's lease epoch (ctx.tailnet.self().epoch), which mgmt
// raises on every deploy and every move. Pass the token to anything the lock
// protects, and have it refuse a token lower than one it has seen.
// @ts-ignore: provided by workerd
import { DurableObject } from "cloudflare:workers";
import { getCaller } from "@tailworks/tailnet";

/** Tokens from lease epoch e are in [e × TOKEN_STRIDE + 1, (e + 1) × TOKEN_STRIDE). */
export const TOKEN_STRIDE = 1_000_000_000;

/** nextToken: the token for the next acquire. */
export function nextToken(lastToken: number, epoch: number): number {
  return Math.max(lastToken + 1, epoch * TOKEN_STRIDE + 1);
}

/** Holder is the verified tailnet identity (WireGuard + WhoIs) of the holder. */
export interface Holder {
  nodeId: string;
  node: string;
  tags: string[];
  user?: string;
}

interface State {
  holder: Holder | null;
  token: number; // the current holder's token; 0 when free
  expiresAt: number; // ms, by this host's clock
  since: number; // when the current holder acquired it
  lastToken: number; // the highest token this object has issued
  why?: "released" | "expired"; // how the last holder lost it
}

const FREE: State = { holder: null, token: 0, expiresAt: 0, since: 0, lastToken: 0 };

function reply(status: number, body: Record<string, unknown>, headers: Record<string, string> = {}): Response {
  return Response.json(body, { status, headers });
}

export class Lock extends (DurableObject as any) {
  declare ctx: any;
  declare env: Env;

  // epoch: the lease epoch of this workload on this host. Off tailworks (no
  // ctx.tailnet) it is 0, and tokens are only monotonic while this object's
  // storage lasts.
  private async epoch(): Promise<number> {
    if (!this.ctx.tailnet) return 0;
    const s = await this.ctx.tailnet.self();
    if (typeof s?.epoch !== "number") throw new Error("ctx.tailnet.self() has no lease epoch (tw-hostd too old for fencing tokens)");
    return s.epoch;
  }

  // load reads the state; a holder whose lease ran out is gone.
  private async load(now: number): Promise<{ s: State; ended?: State }> {
    const s: State = { ...FREE, ...((await this.ctx.storage.get("state")) ?? {}) };
    let ended: State | undefined;
    if (s.holder && now >= s.expiresAt) {
      ended = { ...s, why: "expired" };
      Object.assign(s, { holder: null, token: 0, expiresAt: 0, since: 0, why: "expired" });
    }
    return { s, ended };
  }

  private ttl(asked: unknown): number {
    const def = Number(this.env.DEFAULT_TTL_MS ?? 10000);
    const max = Number(this.env.MAX_TTL_MS ?? 10000);
    const t = typeof asked === "number" && Number.isFinite(asked) && asked > 0 ? asked : def;
    return Math.min(Math.max(Math.round(t), 100), max);
  }

  private async audit(records: Array<Record<string, unknown>>) {
    // `tw audit <lock>` shows who held it. Best effort: an audit hiccup
    // must not fail a lock operation that already happened.
    try {
      if (records.length) await this.ctx.tailnet?.audit(records);
    } catch {}
  }

  async fetch(req: Request): Promise<Response> {
    const name: string = this.ctx.id.name ?? "";
    const op = new URL(req.url).pathname.split("/").filter(Boolean)[2] ?? "status";
    let who: Holder;
    try {
      const c = getCaller(req);
      who = { nodeId: c.nodeId, node: c.node, tags: [...c.tags], ...(c.user ? { user: c.user.login } : {}) };
    } catch (e: any) {
      return reply(401, { error: e?.code ?? "no_identity", detail: "locks are held by tailnet identities: call over the tailnet" });
    }
    let body: any = {};
    if (req.method === "POST") body = (await req.json().catch(() => null)) ?? {};
    else if (op !== "status") return reply(405, { error: "method_not_allowed", detail: op + " is a POST" });
    if (typeof body !== "object") body = {};

    const notes: Array<Record<string, unknown>> = [];
    const res: Response = await this.ctx.blockConcurrencyWhile(async () => {
      let epoch: number;
      try {
        epoch = await this.epoch();
      } catch (e: any) {
        return reply(503, { error: "lock_unavailable", detail: String(e?.message ?? e) }, { "retry-after": "1" });
      }
      const now = Date.now();
      const { s, ended } = await this.load(now);
      if (ended) {
        // The alarm hasn't run yet: record the expiry once, whatever op follows.
        await this.ctx.storage.put("state", s);
        notes.push({ event: "lock_" + ended.why, lock: name, token: ended.token, holder: ended.holder?.node, holderNodeId: ended.holder?.nodeId });
      }
      const view = () => ({ name, held: !!s.holder, holder: s.holder, expiresAt: s.expiresAt || null, since: s.since || null, lastToken: s.lastToken, epoch, why: s.why ?? null });
      const mine = () => !!s.holder && s.token === body.token && s.holder.nodeId === who.nodeId;
      const lost = () => {
        if (s.holder && s.token === body.token) return reply(403, { error: "not_holder", detail: "this token belongs to another tailnet node", ...view() });
        return reply(409, { error: "lock_lost", detail: "this token no longer holds the lock (" + (s.why ?? "superseded") + ")", ...view() });
      };
      let out: Response;
      switch (op) {
        case "status":
          out = reply(200, view());
          break;
        case "acquire": {
          if (s.holder) {
            const ms = Math.max(0, s.expiresAt - now);
            out = reply(409, { error: "lock_held", retryAfterMs: ms, ...view() }, { "retry-after": String(Math.max(1, Math.ceil(ms / 1000))) });
            break;
          }
          const ttlMs = this.ttl(body.ttlMs);
          const token = nextToken(s.lastToken, epoch);
          Object.assign(s, { holder: who, token, lastToken: token, since: now, expiresAt: now + ttlMs, why: undefined });
          await this.ctx.storage.put("state", s);
          await this.ctx.storage.setAlarm(s.expiresAt);
          notes.push({ event: "lock_acquired", lock: name, token, holder: who.node, holderNodeId: who.nodeId, ttlMs });
          out = reply(200, { token, ttlMs, ...view() });
          break;
        }
        case "renew": {
          if (!mine()) {
            out = lost();
            break;
          }
          const ttlMs = this.ttl(body.ttlMs);
          s.expiresAt = now + ttlMs;
          await this.ctx.storage.put("state", s);
          await this.ctx.storage.setAlarm(s.expiresAt);
          out = reply(200, { token: s.token, ttlMs, ...view() });
          break;
        }
        case "release": {
          if (!mine()) {
            out = lost();
            break;
          }
          notes.push({ event: "lock_released", lock: name, token: s.token, holder: who.node, holderNodeId: who.nodeId });
          Object.assign(s, { holder: null, token: 0, expiresAt: 0, since: 0, why: "released" });
          await this.ctx.storage.put("state", s);
          await this.ctx.storage.deleteAlarm();
          out = reply(200, view());
          break;
        }
        default:
          out = reply(404, { error: "not_found", detail: "ops: acquire, renew, release (POST), status (GET)" });
      }
      return out;
    });
    await this.audit(notes);
    return res;
  }

  // alarm frees a lock whose holder stopped renewing, without waiting for
  // the next request to notice.
  async alarm() {
    const notes: Array<Record<string, unknown>> = [];
    await this.ctx.blockConcurrencyWhile(async () => {
      const { s, ended } = await this.load(Date.now());
      if (ended) {
        notes.push({ event: "lock_" + ended.why, lock: this.ctx.id.name, token: ended.token, holder: ended.holder?.node, holderNodeId: ended.holder?.nodeId });
        await this.ctx.storage.put("state", s);
      } else if (s.holder) {
        await this.ctx.storage.setAlarm(s.expiresAt); // renewed since
      }
    });
    await this.audit(notes);
  }
}
