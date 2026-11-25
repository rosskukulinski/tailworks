// tailworks Workers KV (TRUSTED; runs in the platform worker, never in a tenant
// isolate). ADR-037, on the platform-store substrate
// (ADR-044, tw-store.js).
//
// A tenant's env.<BINDING> is workerd's own `kvNamespace` binding, so the
// API (get/getWithMetadata/put/delete/list, types, bulk get) and its
// argument checks are upstream's. The binding speaks HTTP to KVBackend
// (below) with props {kind: "kv", name}; KVBackend turns each request into
// an operation on the namespace's Store object, which keeps the live keys
// in SQLite and journals every change to mgmt before applying it.
//
// Journal entries are internal/kvwire's format (Go reads them too):
//   0x01 | uint32 BE header length | header JSON {k, x?, m?, d?} | value
// with the KV key as the compaction key.
//
// Consistency: a namespace's reads and writes on one host go through one
// object, so they are read-your-writes. Writes by other writers (another
// replica, `tw kv key put`) are seen on the next read that wants fresher
// data than the last sync: by default 60 s (Cloudflare's cacheTtl default),
// or the read's cacheTtl.
import { WorkerEntrypoint } from "cloudflare:workers";
import { registerStoreKind, storeStub, StoreError, toBytes, utf8 } from "tw-store.js";

// Limits (internal/kvwire): Cloudflare's, except values, which are one
// journal entry (≤ 2 MiB) minus room for the header.
export const MAX_KEY = 512;
export const MAX_METADATA = 1024;
export const MAX_VALUE = 2 * 1024 * 1024 - 4096;
export const MAX_LIST = 1000;
export const MAX_BULK = 100;
export const MIN_TTL = 60;
const MIN_CACHE_TTL = 30;
const DEFAULT_FRESH_MS = 60_000;
// An alarm deletes at most this many expired keys per journal append.
const EXPIRE_BATCH = 128;

const te = new TextEncoder();
const nowSec = () => Math.floor(Date.now() / 1000);

export function encodeEntry(h, value) {
  const hj = te.encode(JSON.stringify(h));
  const v = value ?? new Uint8Array(0);
  const out = new Uint8Array(5 + hj.length + v.length);
  out[0] = 1;
  new DataView(out.buffer).setUint32(1, hj.length);
  out.set(hj, 5);
  out.set(v, 5 + hj.length);
  return out;
}

export function decodeEntry(u8) {
  if (u8.length < 5 || u8[0] !== 1) throw new Error(u8.length >= 1 && u8[0] > 1 ? `kv: entry v${u8[0]} was written by a newer tailworks (this one reads v1); upgrade tw-hostd` : "kv: not a v1 KV entry");
  const n = new DataView(u8.buffer, u8.byteOffset, u8.byteLength).getUint32(1);
  if (n > u8.length - 5) throw new Error("kv: truncated KV entry");
  return { h: JSON.parse(utf8(u8.subarray(5, 5 + n))), v: u8.subarray(5 + n) };
}

// HttpError is a KV protocol error: the binding throws
// "KV <OP> failed: <status> <message>" in the tenant.
class HttpError extends Error {
  constructor(status, message) {
    super(message);
    this.status = status;
  }
}

function checkKey(k) {
  if (k === "") throw new HttpError(400, "Key names must not be empty");
  if (k === "." || k === "..") throw new HttpError(400, `Illegal key name "${k}". Please use a different name.`);
  const n = te.encode(k).length;
  if (n > MAX_KEY) throw new HttpError(414, `UTF-8 encoded length of ${n} exceeds key length limit of ${MAX_KEY}.`);
}

function freshMs(cacheTtl) {
  if (cacheTtl == null) return DEFAULT_FRESH_MS;
  const t = Number(cacheTtl);
  if (!Number.isFinite(t) || t < MIN_CACHE_TTL) throw new HttpError(400, `Invalid cache_ttl of ${cacheTtl}. Cache TTL must be at least ${MIN_CACHE_TTL}.`);
  return t * 1000;
}

// ---- the store kind ----

const LIVE = "(x IS NULL OR x > ?)";

function row(s, k) {
  const r = s.sql.exec(`SELECT v, m, x FROM kv_entries WHERE k = ? AND ${LIVE}`, k, nowSec()).toArray();
  if (!r.length) return null;
  return { v: new Uint8Array(r[0].v), m: r[0].m ?? null, x: r[0].x ?? null };
}

function exists(s, k) {
  return s.sql.exec("SELECT 1 FROM kv_entries WHERE k = ?", k).toArray().length > 0;
}

// schedule sets the object's alarm for the next expiry, if any.
async function schedule(s) {
  s.kvSchedule = false;
  const r = s.sql.exec("SELECT MIN(x) AS x FROM kv_entries WHERE x IS NOT NULL").toArray();
  if (r.length && r[0].x != null) await s.wakeAt(r[0].x * 1000);
}

async function afterRead(s) {
  if (s.kvSchedule) await schedule(s);
}

registerStoreKind("kv", {
  init(s) {
    s.sql.exec("CREATE TABLE IF NOT EXISTS kv_entries (k TEXT PRIMARY KEY, v BLOB NOT NULL, m TEXT, x INTEGER, seq INTEGER NOT NULL)");
    s.sql.exec("CREATE INDEX IF NOT EXISTS kv_entries_x ON kv_entries (x) WHERE x IS NOT NULL");
  },
  apply(s, data, seq) {
    const { h, v } = decodeEntry(data);
    if (h.d) {
      s.sql.exec("DELETE FROM kv_entries WHERE k = ?", h.k);
      return;
    }
    s.sql.exec(
      "INSERT INTO kv_entries (k, v, m, x, seq) VALUES (?, ?, ?, ?, ?) ON CONFLICT (k) DO UPDATE SET v = excluded.v, m = excluded.m, x = excluded.x, seq = excluded.seq",
      h.k,
      v.slice().buffer,
      h.m ?? null,
      h.x ?? null,
      seq,
    );
    if (h.x) s.kvSchedule = true;
  },
  reset(s) {
    s.sql.exec("DELETE FROM kv_entries");
  },
  ops: {
    async get(s, key, ms) {
      await s.fresh(ms);
      await afterRead(s);
      return row(s, key);
    },
    async getMany(s, keys, ms) {
      await s.fresh(ms);
      await afterRead(s);
      return keys.map((k) => row(s, k));
    },
    async put(s, key, value, x, m, ttl) {
      if (ttl) x = nowSec() + ttl; // on this object's clock (parsePut, #568)
      const data = encodeEntry(x || m != null ? { k: key, ...(x ? { x } : {}), ...(m != null ? { m } : {}) } : { k: key }, toBytes(value));
      await s.commit([{ ckey: key, data }]);
      if (x) await schedule(s);
    },
    async del(s, key) {
      // A delete of a key this object has never seen may still be a key
      // another writer put: check the journal before writing nothing.
      if (!exists(s, key)) await s.sync();
      await s.commit(() => (exists(s, key) ? [{ ckey: key, data: encodeEntry({ k: key, d: true }) }] : []));
    },
    async list(s, prefix, after, limit, ms) {
      await s.fresh(ms);
      await afterRead(s);
      const rows = s.sql
        .exec(
          `SELECT k, m, x FROM kv_entries WHERE k > ? AND substr(k, 1, length(?)) = ? AND ${LIVE} ORDER BY k LIMIT ?`,
          after,
          prefix,
          prefix,
          nowSec(),
          limit + 1,
        )
        .toArray();
      const more = rows.length > limit;
      const keys = rows.slice(0, limit).map((r) => {
        const o = { name: r.k };
        if (r.x != null) o.expiration = r.x;
        if (r.m != null) o.metadata = r.m; // workerd parses it
        return o;
      });
      return { keys, more };
    },
  },
  // alarm journals a delete for each expired key (so every host and a
  // later catch-up drop it too), then waits for the next expiry. Expired
  // keys are invisible to reads whether or not this has run.
  async alarm(s) {
    try {
      await s.commit(() =>
        s.sql
          .exec("SELECT k FROM kv_entries WHERE x IS NOT NULL AND x <= ? ORDER BY x LIMIT ?", nowSec(), EXPIRE_BATCH)
          .toArray()
          .map((r) => ({ ckey: r.k, data: encodeEntry({ k: r.k, d: true }) })),
      );
    } catch (e) {
      console.warn("tw kv: expiring keys: " + (e?.message ?? e) + "; retrying in 60 s");
      await s.wakeAt(Date.now() + 60_000);
      return;
    }
    await schedule(s);
  },
});

// ---- the binding's backend (workerd's kvNamespace protocol) ----

function storeStatus(e) {
  if (e instanceof HttpError) return [e.status, e.message];
  const msg = String(e?.message ?? e);
  const code = e?.code ?? /^\[([a-z_]+)\]/.exec(msg)?.[1];
  switch (code) {
    case "store_limit":
    case "storage_quota_exceeded":
      return [413, msg];
    case "store_unavailable":
    case "lease_lost":
    case "store_conflict":
    case "identity_pending":
    case "hostd_unavailable":
      return [503, msg];
    case "bad_request":
      return [400, msg];
  }
  return [500, msg];
}

async function drain(body) {
  if (!body || body.locked) return;
  try {
    for await (const _ of body);
  } catch {
    /* the caller went away */
  }
}

// readCapped reads a body of at most max bytes. A longer body is still
// read to the end (and dropped) before the 413, so the binding gets it.
async function readCapped(body, max) {
  if (!body) return new Uint8Array(0);
  const chunks = [];
  let n = 0;
  for await (const c of body) {
    n += c.byteLength;
    if (n <= max) chunks.push(c);
  }
  if (n > max) throw new HttpError(413, `Value length of ${n} exceeds limit of ${max}.`);
  const out = new Uint8Array(n);
  let o = 0;
  for (const c of chunks) {
    out.set(c, o);
    o += c.byteLength;
  }
  return out;
}

function b64url(s) {
  let b = "";
  for (const c of te.encode(s)) b += String.fromCharCode(c);
  return btoa(b).replaceAll("+", "-").replaceAll("/", "_").replaceAll("=", "");
}
function unb64url(s) {
  try {
    const bin = atob(s.replaceAll("-", "+").replaceAll("_", "/"));
    return new TextDecoder("utf-8", { fatal: true }).decode(Uint8Array.from(bin, (c) => c.charCodeAt(0)));
  } catch {
    throw new HttpError(400, "Invalid cursor");
  }
}

// parsePut checks a put's options. An expiration_ttl comes back as ttl,
// not as an absolute x: the store object turns it into one on its own
// clock, the one that later decides the key has expired. A Durable
// Object's Date.now() never goes back while this stateless isolate's
// follows the host's clock, so after a step back an expiry stamped here
// could already be past there (#568).
function parsePut(q, metaHeader) {
  const now = nowSec();
  let x = 0;
  let rel = 0;
  const ttl = q.get("expiration_ttl");
  const exp = q.get("expiration");
  if (ttl !== null) {
    const t = Number(ttl);
    if (!Number.isInteger(t) || t <= 0) throw new HttpError(400, `Invalid expiration_ttl of ${ttl}. Please specify integer greater than 0.`);
    if (t < MIN_TTL) throw new HttpError(400, `Invalid expiration_ttl of ${ttl}. Expiration TTL must be at least ${MIN_TTL}.`);
    rel = t;
  } else if (exp !== null) {
    x = Number(exp);
    if (!Number.isInteger(x) || x <= now)
      throw new HttpError(400, `Invalid expiration of ${exp}. Please specify integer greater than the current number of seconds since the UNIX epoch.`);
    if (x < now + MIN_TTL) throw new HttpError(400, `Invalid expiration of ${exp}. Expiration times must be at least ${MIN_TTL} seconds in the future.`);
  }
  let m = null;
  if (metaHeader !== null) {
    const n = te.encode(metaHeader).length;
    if (n > MAX_METADATA) throw new HttpError(413, `Metadata length of ${n} exceeds limit of ${MAX_METADATA}.`);
    try {
      JSON.parse(metaHeader);
    } catch {
      throw new HttpError(400, "Metadata must be JSON");
    }
    m = metaHeader;
  }
  return { x, ttl: rel, m };
}

function typed(r, type, withMetadata) {
  if (r == null) return null;
  let v = utf8(r.v);
  if (type === "json") {
    try {
      v = JSON.parse(v);
    } catch {
      throw new HttpError(400, "At least one of the requested keys corresponds to a non-json value");
    }
  }
  return withMetadata ? { value: v, metadata: r.m == null ? null : JSON.parse(r.m) } : v;
}

export class KVBackend extends WorkerEntrypoint {
  #store() {
    const p = this.ctx.props ?? {};
    if (p.kind !== "kv" || typeof p.name !== "string") throw new HttpError(500, "kv: binding without a namespace");
    return storeStub(this.ctx.exports, "kv", p.name);
  }

  async fetch(req) {
    try {
      return await this.#serve(req);
    } catch (e) {
      // workerd's put() waits until its request body has been read to
      // the end (cancel() is not enough; measured on workerd 2026-11-08):
      // drain a body refused before it was read.
      await drain(req.body);
      const [status, msg] = storeStatus(e);
      // workerd shows statusText for GETs and the body for PUTs.
      return new Response(msg, { status, statusText: msg.replace(/[^\x20-\x7e]/g, "?").slice(0, 256) });
    }
  }

  async #serve(req) {
    const u = new URL(req.url);
    const q = u.searchParams;
    const s = this.#store();
    if (u.pathname === "/" && req.method === "GET") {
      const limit = q.get("key_count_limit") === null ? MAX_LIST : Number(q.get("key_count_limit"));
      if (!Number.isInteger(limit) || limit < 1 || limit > MAX_LIST)
        throw new HttpError(400, `Invalid key_count_limit of ${q.get("key_count_limit")}. Please specify an integer from 1 to ${MAX_LIST}.`);
      const prefix = q.get("prefix") ?? "";
      if (te.encode(prefix).length > MAX_KEY) throw new HttpError(414, `UTF-8 encoded length of the prefix exceeds key length limit of ${MAX_KEY}.`);
      const cursor = q.get("cursor");
      const after = cursor ? unb64url(cursor) : "";
      const { keys, more } = await s.op("list", [prefix, after, limit, freshMs(q.get("cache_ttl"))]);
      const res = { keys, list_complete: !more, cacheStatus: null };
      if (more) res.cursor = b64url(keys[keys.length - 1].name);
      return Response.json(res);
    }
    if (u.pathname === "/bulk/get" && req.method === "POST") {
      let b;
      try {
        b = JSON.parse(utf8(await readCapped(req.body, 1 << 20)));
      } catch {
        throw new HttpError(400, "bulk get: the body must be JSON");
      }
      const keys = b?.keys;
      if (!Array.isArray(keys) || keys.length < 1) throw new HttpError(400, "You must request a minimum of 1 key");
      if (keys.length > MAX_BULK) throw new HttpError(400, `You can request a maximum of ${MAX_BULK} keys`);
      const type = b.type ?? "text";
      if (type !== "text" && type !== "json") throw new HttpError(400, `"${type}" is not a valid type. Use "json" or "text"`);
      for (const k of keys) checkKey(String(k));
      const rows = await s.op("getMany", [keys.map(String), freshMs(b.cacheTtl)]);
      const out = {};
      keys.forEach((k, i) => (out[k] = typed(rows[i], type, !!b.withMetadata)));
      return Response.json(out);
    }
    let key = u.pathname.slice(1);
    if ((q.get("urlencoded") ?? "").toLowerCase() === "true") {
      try {
        key = decodeURIComponent(key);
      } catch {
        throw new HttpError(400, "Could not URL-decode key name");
      }
    }
    checkKey(key);
    switch (req.method) {
      case "GET": {
        const r = await s.op("get", [key, freshMs(q.get("cache_ttl"))]);
        if (!r) return new Response("Not Found", { status: 404 });
        const h = new Headers();
        if (r.x != null) h.set("CF-Expiration", String(r.x));
        if (r.m != null) h.set("CF-KV-Metadata", r.m);
        return new Response(r.v, { headers: h });
      }
      case "PUT": {
        const v = await readCapped(req.body, MAX_VALUE); // first: see drain()
        const { x, ttl, m } = parsePut(q, req.headers.get("cf-kv-metadata"));
        await s.op("put", [key, v, x, m, ttl]);
        return new Response(null);
      }
      case "DELETE":
        await s.op("del", [key]);
        return new Response(null);
    }
    throw new HttpError(405, "Method not allowed");
  }
}

// KVFacet is a facet's env.<BINDING> (a Dynamic Worker can't hold a native
// KV binding): an RPC stub whose methods are KVNamespace's, forwarded to
// the platform's native binding of the same namespace, so a facet gets the
// same API, argument checks and errors.
export class KVFacet extends WorkerEntrypoint {
  #kv() {
    const b = this.ctx.props?.binding;
    const kv = typeof b === "string" ? this.env["__TW_STORE_" + b] : undefined;
    if (!kv) throw new StoreError("bad_request", "kv: no such binding");
    return kv;
  }
  get(key, opts) {
    return this.#kv().get(key, opts);
  }
  getWithMetadata(key, opts) {
    return this.#kv().getWithMetadata(key, opts);
  }
  put(key, value, opts) {
    return this.#kv().put(key, value, opts);
  }
  delete(key) {
    return this.#kv().delete(key);
  }
  list(opts) {
    return this.#kv().list(opts);
  }
}
