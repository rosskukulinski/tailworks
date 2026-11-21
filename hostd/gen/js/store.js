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
// acknowledged write survives the host.
//
// An entry the kind can't apply (apply throws: an entry written by a
// newer tailworks, a corrupt entry, a D1 statement that fails on replay)
// stops the replay there: seq stays at the entry before it, the store is
// marked degraded (the failing seq and error, kept on disk), and it never
// reports itself synced past it. A degraded store keeps serving reads
// from what it has applied, refuses writes (store_degraded), and tries
// the entry again on later syncs; it heals if the entry applies.
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
    });
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
      const entries = (p.entries ?? []).map((e) => ({ seq: e.seq, data: b64dec(e.data) }));
      let bad = null;
      this.ctx.storage.transactionSync(() => {
        bad = this.#applyEntries(entries);
        if (bad) return; // keep what applied; never synced past the failing entry
        if (!p.more && p.head > this.seq) this.#setMeta("seq", p.head);
        if (!p.more) this.#setMeta("synced", 1);
      });
      if (bad) throw this.#degradedError(bad);
      if (!p.more) break;
      after = this.seq;
    }
    this.syncedAt = Date.now();
  }

  // sync catches up with the journal now.
  sync() {
    this.#check();
    return this.#locked(() => this.#catchUp());
  }

  // fresh makes sure the last catch-up is at most maxAgeMs old. If the
  // journal can't be reached, a store that has synced before serves what
  // it has (fail static, like a Worker during a mgmt outage); one that
  // never has refuses (store_unavailable).
  async fresh(maxAgeMs = DEFAULT_FRESH_MS) {
    this.#check();
    if (this.syncedAt && Date.now() - this.syncedAt < maxAgeMs) return;
    // A degraded store tries its failing entry again at the same pace.
    if (this.degraded && Date.now() - this.triedAt < maxAgeMs) return;
    try {
      await this.sync();
    } catch (e) {
      // Degraded: what it has applied is an exact prefix of the journal.
      if (e?.code === "store_degraded") return;
      if (!this.everSynced || e?.code === "lease_lost") throw e;
      console.warn("tw store " + this.kindName + "/" + this.name + ": serving local data; journal unreachable: " + e.message);
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
      if (!this.syncedAt || this.degraded) await this.#catchUp();
      for (let attempt = 0; attempt < 8; attempt++) {
        const es = (typeof entries === "function" ? entries() : entries).map((e) => ({ ckey: e.ckey || undefined, data: toBytes(e.data) }));
        if (!es.length) return this.seq;
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
        if (bad) throw this.#degradedError(bad, true);
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
