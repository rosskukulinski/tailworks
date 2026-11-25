// Client for the lock service (src/index.ts), for any Worker, Durable
// Object or Node process on the tailnet. Copy this file into the app that
// takes the lock; it depends on nothing but fetch.
//
//   const locks = lockClient({ url: "http://locks" }); // the lock workload's MagicDNS name
//   await locks.withLock("job:nightly-report", async (l) => {
//     await db.write(report, { fencingToken: l.token }); // the store refuses older tokens
//   });
//
// A lease is only as good as its deadline: past `deadline` (this machine's
// clock, counted from when the granting request was SENT), another client
// may hold the lock. withLock renews well before then and aborts `signal`
// as soon as the lock can no longer be trusted. For writes to other
// systems, rely on the fencing token, not on timing (docs/locks.md).

export interface Holder {
  nodeId: string;
  node: string;
  tags: string[];
  user?: string;
}

export interface Lease {
  name: string;
  /** Fencing token: strictly greater than every token issued before it. */
  token: number;
  ttlMs: number;
  /** When the lock service will let the lease go (the service's clock). */
  expiresAt: number;
  /** Stop relying on the lock after this (this machine's clock). */
  deadline: number;
  holder: Holder;
}

export interface LockStatus {
  name: string;
  held: boolean;
  holder: Holder | null;
  expiresAt: number | null;
  lastToken: number;
  epoch: number;
  why: "released" | "expired" | null;
}

export class LockError extends Error {
  /** lock_held, lock_lost, not_holder, lease_expired, lock_unavailable, killed (the lock object was killed), … */
  readonly code: string;
  readonly status: number;
  readonly body: Record<string, any>;
  constructor(code: string, status: number, message: string, body: Record<string, any> = {}) {
    super(message);
    this.name = "LockError";
    this.code = code;
    this.status = status;
    this.body = body;
  }
}

export interface LockClientOptions {
  /** Base URL of the lock workload, e.g. "http://locks" or "http://locks.<tailnet>.ts.net". */
  url: string;
  fetch?: typeof fetch;
  /** Lease length to ask for (the service caps it at its MAX_TTL_MS). */
  ttlMs?: number;
}

export interface AcquireOptions {
  ttlMs?: number;
  /** Keep trying this long while someone else holds the lock (0: one try). */
  waitMs?: number;
  signal?: AbortSignal;
}

export interface WithLockOptions extends AcquireOptions {
  /** How often to renew (default: a third of the lease). */
  renewEveryMs?: number;
}

/** What withLock passes to its callback. */
export interface Held {
  name: string;
  token: number;
  /** Aborted when the lock is lost or its deadline passes unrenewed. */
  signal: AbortSignal;
  /** The current lease (its deadline moves on every renewal). */
  lease(): Lease;
}

const sleep = (ms: number, signal?: AbortSignal) =>
  new Promise<void>((res, rej) => {
    if (signal?.aborted) return rej(signal.reason);
    const t = setTimeout(res, ms);
    signal?.addEventListener("abort", () => (clearTimeout(t), rej(signal.reason)), { once: true });
  });

export function lockClient(opts: LockClientOptions) {
  const f = opts.fetch ?? ((input: RequestInfo | URL, init?: RequestInit) => fetch(input, init));
  const base = opts.url.replace(/\/+$/, "");

  async function call(name: string, op: string, body?: Record<string, unknown>): Promise<{ status: number; body: any; sentAt: number }> {
    const sentAt = Date.now();
    let r: Response;
    try {
      r = await f(base + "/locks/" + encodeURIComponent(name) + (op === "status" ? "" : "/" + op), {
        method: op === "status" ? "GET" : "POST",
        headers: { "content-type": "application/json" },
        body: op === "status" ? undefined : JSON.stringify(body ?? {}),
      });
    } catch (e: any) {
      throw new LockError("lock_unavailable", 0, "lock service unreachable: " + (e?.message ?? e));
    }
    const text = await r.text();
    let b: any;
    try {
      b = JSON.parse(text);
    } catch {
      b = { detail: text };
    }
    // tailworks' own errors (a killed lock object: 410 killed) carry Tw-Error.
    if (b && typeof b === "object" && !b.error && r.status !== 200) b.error = r.headers.get("tw-error") ?? b.code ?? undefined;
    return { status: r.status, body: b, sentAt };
  }

  const fail = (r: { status: number; body: any }) =>
    new LockError(r.body?.error ?? (r.status >= 500 ? "lock_unavailable" : "lock_error"), r.status, r.body?.detail ?? r.body?.error ?? "HTTP " + r.status, r.body);

  const lease = (name: string, r: { body: any; sentAt: number }): Lease => ({
    name,
    token: r.body.token,
    ttlMs: r.body.ttlMs,
    expiresAt: r.body.expiresAt,
    // Counted from when the request was sent, so the client stops trusting
    // the lease no later than the service lets it go, whatever the clocks say.
    deadline: r.sentAt + r.body.ttlMs,
    holder: r.body.holder,
  });

  /** tryAcquire: one attempt; null when someone else holds the lock. */
  async function tryAcquire(name: string, o: { ttlMs?: number } = {}): Promise<Lease | null> {
    const r = await call(name, "acquire", { ttlMs: o.ttlMs ?? opts.ttlMs });
    if (r.status === 200) return lease(name, r);
    if (r.status === 409 && r.body?.error === "lock_held") return null;
    throw fail(r);
  }

  /** acquire: wait up to waitMs for the lock; LockError lock_held if it stays taken. */
  async function acquire(name: string, o: AcquireOptions = {}): Promise<Lease> {
    const until = Date.now() + (o.waitMs ?? 0);
    for (;;) {
      const r = await call(name, "acquire", { ttlMs: o.ttlMs ?? opts.ttlMs });
      if (r.status === 200) return lease(name, r);
      const left = until - Date.now();
      if (r.status !== 409 || r.body?.error !== "lock_held" || left <= 0) throw fail(r);
      const after = typeof r.body.retryAfterMs === "number" ? r.body.retryAfterMs : 1000;
      await sleep(Math.min(left, Math.max(50, Math.min(after, 1000)) + Math.random() * 100), o.signal);
    }
  }

  /** renew: extend the lease; LockError lock_lost / not_holder if it is gone. */
  async function renew(l: Lease, o: { ttlMs?: number } = {}): Promise<Lease> {
    const r = await call(l.name, "renew", { token: l.token, ttlMs: o.ttlMs ?? l.ttlMs });
    if (r.status !== 200) throw fail(r);
    return lease(l.name, r);
  }

  /** release: give the lock up (a lease that is already gone is not an error). */
  async function release(l: Lease): Promise<void> {
    const r = await call(l.name, "release", { token: l.token });
    if (r.status !== 200 && !(r.status === 409 && r.body?.error === "lock_lost")) throw fail(r);
  }

  async function status(name: string): Promise<LockStatus> {
    const r = await call(name, "status");
    if (r.status !== 200) throw fail(r);
    return r.body as LockStatus;
  }

  /**
   * withLock runs fn while holding the lock, renewing it in the background,
   * and releases it afterwards. If the lock is lost while fn runs, `signal`
   * is aborted and withLock throws LockError (lock_lost or lease_expired)
   * once fn returns: anything fn did after that ran without the lock.
   */
  async function withLock<T>(name: string, fn: (h: Held) => Promise<T>, o: WithLockOptions = {}): Promise<T> {
    let cur = await acquire(name, o);
    const ac = new AbortController();
    const every = o.renewEveryMs ?? Math.max(50, Math.floor(cur.ttlMs / 3));
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let expiry: ReturnType<typeof setTimeout> | undefined;
    const lose = (e: LockError) => {
      if (!ac.signal.aborted) ac.abort(e);
    };
    const expired = () => lose(new LockError("lease_expired", 0, "lease deadline passed without a successful renewal"));
    const arm = () => {
      if (expiry !== undefined) clearTimeout(expiry);
      expiry = setTimeout(expired, Math.max(0, cur.deadline - Date.now()));
    };
    const tick = async () => {
      if (stopped || ac.signal.aborted) return;
      try {
        cur = await renew(cur);
        arm();
      } catch (e: any) {
        // Lost for sure: stop now. Unreachable: keep trying until the deadline.
        if (e instanceof LockError && e.code !== "lock_unavailable") return lose(e);
      }
      if (!stopped && !ac.signal.aborted) timer = setTimeout(tick, every);
    };
    arm();
    timer = setTimeout(tick, every);
    try {
      const out = await fn({ name, token: cur.token, signal: ac.signal, lease: () => cur });
      // Check the deadline itself, not only the expiry timer: on a busy
      // event loop the timer can still be pending after the deadline has
      // passed, and then fn's work may have outlived the lease (#196).
      if (Date.now() >= cur.deadline) expired();
      if (ac.signal.aborted) throw ac.signal.reason;
      return out;
    } finally {
      stopped = true;
      clearTimeout(timer);
      if (expiry !== undefined) clearTimeout(expiry);
      await release(cur).catch(() => {}); // a lease that is already gone is fine
    }
  }

  return { acquire, tryAcquire, renew, release, status, withLock };
}

export type LockClient = ReturnType<typeof lockClient>;
