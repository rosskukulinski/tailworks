// tailworks platform stores (TRUSTED; runs in the platform worker, never in a
// tenant isolate). ADR-044.
//
// A store is one platform-owned SQLite Durable Object per (kind, name) of a
// workload: `Store`, addressed as getByName(kind + ":" + name). It is the
// backend of a storage binding: workerd's own binding (e.g. kvNamespace)
// or a service binding targets a kind's backend entrypoint in this worker,
// with props {kind, name} that tenant code can't change, and the backend
// calls the Store object.
//
// Durability: every change is a journal entry. Store.commit() appends the
// entries to mgmt's journal (through hostd, which adds the tenant, the
// workload and the lease epoch; mgmt fences it to the lease holder) BEFORE
// applying them to its SQLite, and applies the same entries the same way
// when it catches up (on its first use after a restart or a move to
// another host, when an append finds another writer got in first, and
// when a read finds its last sync older than the kind's freshness). So
// local state is always a replay of a prefix of the journal, and an
// acknowledged write survives the host. A read waits a bounded time for
// its catch-up and then serves the local copy (fresh(), READ_WAIT_MS);
// a write never goes around the journal.
//
// An entry the kind can't apply (apply throws: an entry written by a
// newer tailworks, a corrupt entry, a D1 statement that fails on replay)
// stops the replay there: seq stays at the entry before it, the store is
// marked degraded (the failing seq and error, kept on disk), and it never
// reports itself synced past it. A degraded store keeps serving reads
// from what it has applied, refuses writes (store_degraded), and tries
// the entry again on later syncs; it heals if the entry applies (or an
// operator skips it: `tw d1 repair --skip <seq>` deletes it from the
// journal). It reports each change to mgmt (#report), which shows it on
// the store and audits it.
//
// A kind is a module that calls registerStoreKind(kind, def):
//   init(store)              create its tables (idempotent; every boot)
//   apply(store, data, seq)  apply one entry (Uint8Array) inside a transaction;
//                            the only place a kind changes its tables
//   reset(store)             drop all its data (the store was deleted/recreated)
//   ops: { name(store, ...args) }  what its backend calls (Store.op)
//   adminOps: { name(store, ...args) }  optional: operator operations
//                            (the platform's admin route, Store.adminOp)
//   alarm(store)             optional: the object's alarm fired
// and exports its backend entrypoint class(es).
import { DurableObject } from "cloudflare:workers";

const KINDS = new Map();

export function registerStoreKind(kind, def) {
  KINDS.set(kind, def);
}

// StoreError carries a typed code; across RPC the message keeps it.
export class StoreError extends Error {
  constructor(code, message) {
    super("[" + code + "] " + message);
    this.code = code;
  }
}

const te = new TextEncoder();
const td = new TextDecoder();

export function toBytes(v) {
  if (v instanceof Uint8Array) return v;
  if (typeof v === "string") return te.encode(v);
  if (v instanceof ArrayBuffer) return new Uint8Array(v);
  if (ArrayBuffer.isView(v)) return new Uint8Array(v.buffer, v.byteOffset, v.byteLength);
  throw new TypeError("not bytes");
}
export const utf8 = (u8) => td.decode(u8);

function b64enc(u8) {
  if (typeof u8.toBase64 === "function") return u8.toBase64();
  let s = "";
  for (let i = 0; i < u8.length; i += 0x8000) s += String.fromCharCode.apply(null, u8.subarray(i, i + 0x8000));
  return btoa(s);
}
function b64dec(s) {
  if (typeof Uint8Array.fromBase64 === "function") return Uint8Array.fromBase64(s);
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

// DEFAULT_FRESH_MS: how old a sync may be before a read catches up first
// (Cloudflare KV's default cacheTtl is 60 s).
export const DEFAULT_FRESH_MS = 60_000;

// READ_WAIT_MS: how long a read that wants fresher data (fresh()) waits
// for the catch-up with mgmt's journal before it serves the local copy
// instead. A catch-up is normally one round trip to mgmt, milliseconds on
// a tailnet; 2 s is far past that, yet short enough that a slow or
// black-holed mgmt doesn't stall the workload's reads. The catch-up
// itself carries on (hostd gives up on a journal read after 10 s,
// storeReadTimeout in hostd/stores.go).
export const READ_WAIT_MS = 2000;

// STALE_RETRY_MS: once a catch-up for a read has failed or outlasted
// READ_WAIT_MS, the journal counts as unreachable (the breaker is open):
// reads serve the local copy at once, without waiting, and start one
// catch-up in the background at most this often. The first catch-up that
// succeeds closes the breaker. Writes are not affected: they always go
// to the journal first, and fail while it can't be reached.
export const STALE_RETRY_MS = 5000;

// unreachable: e means the journal could not be reached (mgmt, the path
// to it, or hostd), not that it answered.
function unreachable(e) {
  return !["store_degraded", "lease_lost", "store_conflict", "bad_request", "unknown_store"].includes(e?.code);
}

export class Store extends DurableObject {
  constructor(ctx, env) {
    super(ctx, env);
    const n = ctx.id.name ?? "";
    const i = n.indexOf(":");
    this.kindName = i > 0 ? n.slice(0, i) : "";
    this.name = i > 0 ? n.slice(i + 1) : "";
    this.kind = KINDS.get(this.kindName);
    this.sql = ctx.storage.sql;
    // The substrate's own state lives in the synchronous KV API (workerd's
    // hidden _cf_KV table, in the same SQLite file and transactions), so a
    // kind's SQL namespace holds only its own tables. D1 hands that
    // namespace to tenant SQL.
    this.kv = ctx.storage.kv;
    if (this.kind) this.kind.init(this);
    this.syncedAt = 0; // last successful catch-up of this boot (ms)
    this.triedAt = 0; // last catch-up that stopped at a failing entry (ms)
    this.chain = Promise.resolve();
    // Catch-ups started by sync() (concurrent ones coalesce): queued is
    // one waiting its turn in the chain (it covers anyone who asks before
    // it starts); running is {p, at} for the one in progress, started at
    // `at`.
    this.queued = null;
    this.running = null;
    // The read breaker (fresh): when the journal last proved unreachable
    // (0: closed), and when a read last started a background catch-up.
    this.staleSince = 0;
    this.retriedAt = 0;
  }

  #meta(k, d) {
    return this.kv.get("__tw_store_" + k) ?? d;
  }
  #setMeta(k, v) {
    this.kv.put("__tw_store_" + k, v);
  }
  get seq() {
    return Number(this.#meta("seq", 0));
  }
  get incarnation() {
    return String(this.#meta("inc", ""));
  }
  // everSynced: this object has caught up with the journal at least once
  // (on this host's disk). Until then its tables mean nothing, so reads
  // fail rather than answer from an empty or stale copy.
  get everSynced() {
    return this.#meta("synced", 0) === 1;
  }

  // degraded: the journal entry this object could not apply, {seq, error,
  // at}, or null. Replay stopped before it.
  get degraded() {
    const d = this.#meta("degraded", "");
    return d ? JSON.parse(d) : null;
  }

  #check() {
    if (!this.kind) throw new StoreError("bad_request", "unknown store kind " + this.kindName);
  }

  // locked runs fn after every earlier commit/sync (they await hostd).
  #locked(fn) {
    const p = this.chain.then(fn, fn);
    this.chain = p.catch(() => {});
    return p;
  }

  async #hostd(op, body) {
    let r;
    try {
      r = await this.env.HOSTD.fetch("http://hostd/v1/store/" + op, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ kind: this.kindName, name: this.name, ...body }),
      });
    } catch (e) {
      throw new StoreError("store_unavailable", "hostd: " + String(e?.message ?? e));
    }
    let j = null;
    try {
      j = await r.json();
    } catch {
      /* not JSON */
    }
    if (!r.ok) throw new StoreError(j?.code ?? "store_unavailable", j?.message ?? "store journal answered " + r.status);
    return j;
  }

  #wipe(inc) {
    this.ctx.storage.transactionSync(() => {
      this.kind.reset(this);
      this.#setMeta("seq", 0);
      this.#setMeta("inc", inc);
      this.kv.delete("__tw_store_degraded");
      this.kv.delete("__tw_store_reported"); // a new store: mgmt has no record of it
    });
  }

  // #report tells mgmt (through hostd) that this object is degraded, or
  // healthy again, once per change: "reported" is the failing seq mgmt was
  // last told ("" = healthy). mgmtDegraded is mgmt's record from the last
  // journal page: a healthy object that has applied up to it (on a new
  // host after a move) clears it. Best effort: a failed report is retried
  // by the next sync that ends the same way.
  async #report(mgmtDegraded = null) {
    const d = this.degraded;
    const want = d ? String(d.seq) : "";
    const stale = !d && mgmtDegraded && this.seq >= mgmtDegraded.seq;
    if (want === String(this.#meta("reported", "")) && !stale) return;
    try {
      await this.#hostd("status", { incarnation: this.incarnation, applied: this.seq, degraded: d ?? undefined });
      this.#setMeta("reported", want);
    } catch (e) {
      console.warn("tw store " + this.kindName + "/" + this.name + ": could not report its state to mgmt: " + String(e?.message ?? e));
    }
  }

  // #applyEntries applies [{seq, data}] in order, inside the caller's
  // transaction, each in its own nested one, and moves seq past each
  // entry it applied. At the first entry the kind can't apply it stops,
  // marks the store degraded and returns {seq, error}; else null.
  #applyEntries(entries) {
    for (const e of entries) {
      try {
        this.ctx.storage.transactionSync(() => this.kind.apply(this, e.data, e.seq));
      } catch (err) {
        const bad = { seq: e.seq, error: String(err?.message ?? err), at: Date.now() };
        this.#setMeta("degraded", JSON.stringify(bad));
        return bad;
      }
      this.#setMeta("seq", e.seq);
    }
    const d = this.degraded;
    if (d && this.seq >= d.seq) this.kv.delete("__tw_store_degraded"); // the entry applied this time
    return null;
  }

  #degradedError(bad, journaled = false) {
    this.triedAt = Date.now();
    const msg =
      `${this.kindName} store ${this.name}: journal entry ${bad.seq} failed to apply (${bad.error}); ` +
      (journaled ? "the write is in the journal but this host could not apply it; " : "") +
      `the local copy stops before it: reads serve it, writes are refused until the entry applies or the store is recreated`;
    console.error("tw store " + msg);
    return new StoreError("store_degraded", msg);
  }

  async #catchUp() {
    let after = this.seq;
    let mgmtDegraded = null;
    for (;;) {
      const p = await this.#hostd("read", { after });
      const inc = p.incarnation ?? "";
      if (inc !== this.incarnation) {
        if (this.seq !== 0 || this.incarnation !== "") {
          // The store was deleted (inc "") or deleted and created again:
          // what this object holds belongs to no live store.
          this.#wipe(inc);
          if (after !== 0) {
            after = 0;
            continue;
          }
        } else {
          this.#setMeta("inc", inc);
        }
      }
      if (inc !== "" && (p.head ?? 0) < this.seq) {
        // The journal is behind this copy: mgmt's store was restored from
        // a backup (tw restore, an upgrade's rollback). The journal is
        // authoritative, so start over from it; otherwise every write
        // would conflict with a head this copy is already past.
        this.#wipe(inc);
        after = 0;
        continue;
      }
      mgmtDegraded = p.degraded ?? null;
      const entries = (p.entries ?? []).map((e) => ({ seq: e.seq, data: b64dec(e.data) }));
      let bad = null;
      this.ctx.storage.transactionSync(() => {
        bad = this.#applyEntries(entries);
        if (bad) return; // keep what applied; never synced past the failing entry
        if (!p.more && p.head > this.seq) this.#setMeta("seq", p.head);
        if (!p.more) this.#setMeta("synced", 1);
        // The failing entry left the journal (an operator skipped it) with
        // nothing after it to apply.
        const d = this.degraded;
        if (!p.more && d && this.seq >= d.seq) this.kv.delete("__tw_store_degraded");
      });
      if (bad) {
        await this.#report();
        throw this.#degradedError(bad);
      }
      if (!p.more) break;
      after = this.seq;
    }
    this.syncedAt = Date.now();
    await this.#report(mgmtDegraded);
  }

  // sync catches up with the journal: with a catch-up that has not
  // started yet if one is queued (it covers this caller too), else with a
  // new one after every earlier commit/sync. It waits as long as that
  // takes (hostd bounds each journal read): sync() is for callers that
  // must see the journal (a D1 session bookmark, "first-primary").
  sync() {
    this.#check();
    if (this.queued) return this.queued;
    const p = this.#locked(async () => {
      if (this.queued === p) this.queued = null;
      const run = { p, at: Date.now() };
      this.running = run;
      try {
        await this.#catchUp();
        this.staleSince = 0;
      } catch (e) {
        if (!unreachable(e)) this.staleSince = 0;
        else this.#openBreaker(e.message);
        throw e;
      } finally {
        if (this.running === run) this.running = null;
      }
    });
    this.queued = p;
    return p;
  }

  #openBreaker(why) {
    if (this.staleSince) return;
    this.staleSince = Date.now();
    this.retriedAt = this.staleSince;
    console.warn("tw store " + this.kindName + "/" + this.name + ": journal unreachable (" + why + "); reads serve local data until it answers");
  }

  // fresh makes sure the last catch-up is at most maxAgeMs old, waiting at
  // most READ_WAIT_MS for it. If the journal is slow or can't be reached,
  // a store that has synced before serves what it has (fail static) and
  // later reads don't wait for it (STALE_RETRY_MS); one that never has
  // waits for the catch-up and refuses if it fails (store_unavailable).
  // lease_lost is never served around.
  async fresh(maxAgeMs = DEFAULT_FRESH_MS) {
    this.#check();
    const now = Date.now();
    if (this.syncedAt && now - this.syncedAt < maxAgeMs) return;
    // A degraded store tries its failing entry again at the same pace.
    if (this.degraded && now - this.triedAt < maxAgeMs) return;
    const local = this.everSynced;
    if (this.staleSince && local) {
      // The breaker is open: serve local data now; retry in the background.
      if (!this.queued && !this.running && now - this.retriedAt >= STALE_RETRY_MS) {
        this.retriedAt = now;
        this.sync().catch(() => {});
      }
      return;
    }
    // Share a catch-up already in progress if it started recently enough.
    const r = this.running;
    const p = !this.queued && r && r.at >= now - maxAgeMs ? r.p : this.sync();
    try {
      if (!local) return await p; // nothing to serve meanwhile
      let timer;
      const late = new Promise((res) => (timer = setTimeout(() => res(true), READ_WAIT_MS)));
      const timedOut = await Promise.race([p.then(() => false), late]).finally(() => clearTimeout(timer));
      if (timedOut) {
        p.catch(() => {}); // it carries on; sync() records how it ends
        this.#openBreaker("no answer within " + READ_WAIT_MS + " ms");
      }
    } catch (e) {
      // Degraded: what it has applied is an exact prefix of the journal.
      if (e?.code === "store_degraded") return;
      if (!local || e?.code === "lease_lost") throw e;
      if (!unreachable(e)) console.warn("tw store " + this.kindName + "/" + this.name + ": serving local data; catch-up failed: " + e.message);
      // else sync() opened the breaker and logged it.
    }
  }

  // commit appends entries to the journal, then applies them. entries is
  // [{ckey?, data}] (data: string or bytes), or a function returning it,
  // re-run after a conflict so read-modify-write kinds see the new state
  // (return [] to write nothing). Returns the new seq.
  commit(entries) {
    this.#check();
    return this.#locked(async () => {
      // A degraded store tries its failing entry again first, and refuses
      // the write (store_degraded) if it still doesn't apply.
      let caughtUp = !this.syncedAt || this.degraded;
      if (caughtUp) await this.#catchUp();
      for (let attempt = 0; attempt < 8; attempt++) {
        const es = (typeof entries === "function" ? entries() : entries).map((e) => ({ ckey: e.ckey || undefined, data: toBytes(e.data) }));
        if (!es.length) {
          // Nothing to write, as decided on the local copy (a delete of
          // a key it doesn't have, an UPDATE that matches no row). Only
          // an append is checked against the journal's head, and the
          // copy can be behind it (another replica wrote; mgmt can't be
          // reached), so confirm the decision on a caught-up copy before
          // acknowledging it (#562). If the journal can't be reached the
          // catch-up throws and the write fails, as every write does then.
          if (caughtUp) return this.seq;
          await this.#catchUp();
          caughtUp = true;
          continue;
        }
        let r;
        try {
          r = await this.#hostd("append", {
            after: this.seq,
            incarnation: this.incarnation,
            entries: es.map((e) => ({ ckey: e.ckey, data: b64enc(e.data) })),
          });
        } catch (e) {
          if (e.code === "store_conflict") {
            await this.#catchUp();
            continue;
          }
          throw e;
        }
        let bad = null;
        this.ctx.storage.transactionSync(() => {
          let seq = r.head - es.length;
          this.#setMeta("inc", r.incarnation);
          bad = this.#applyEntries(es.map((e) => ({ seq: ++seq, data: e.data })));
          if (!bad) this.#setMeta("synced", 1);
        });
        if (bad) {
          await this.#report();
          throw this.#degradedError(bad, true);
        }
        return r.head;
      }
      throw new StoreError("store_conflict", "the store kept moving under this write; retry");
    });
  }

  // wakeAt asks for alarm() at time t (ms) unless an earlier one is set.
  async wakeAt(t) {
    const cur = await this.ctx.storage.getAlarm();
    if (cur == null || t < cur) await this.ctx.storage.setAlarm(t);
  }

  async alarm() {
    if (this.kind?.alarm) await this.kind.alarm(this);
  }

  // op runs one of the kind's operations (called by its backend).
  async op(name, args = []) {
    this.#check();
    const f = this.kind.ops?.[name];
    if (typeof f !== "function") throw new StoreError("bad_request", "unknown " + this.kindName + " operation " + name);
    return f(this, ...args);
  }

  // adminOp runs one of the kind's operator operations (def.adminOps:
  // `tw d1 execute`, exports, …). Only the platform's admin route calls
  // it: hostd sends it for mgmt's admin API, for a store the running
  // deployment declares.
  async adminOp(name, args = []) {
    this.#check();
    const f = this.kind.adminOps?.[name];
    if (typeof f !== "function") throw new StoreError("bad_request", "unknown " + this.kindName + " admin operation " + name);
    return f(this, ...args);
  }
}

// storeStub is the Store object of (kind, name) for a backend entrypoint.
export function storeStub(exportsOrCtx, kind, name) {
  return exportsOrCtx.Store.getByName(kind + ":" + name);
}
