// tailworks R2 buckets (TRUSTED; platform worker). ADR-036.
//
// A bucket is a platform store of kind "r2" (tw-store.js): its Store
// object holds the INDEX (key → version, size, etag, upload time, HTTP and
// custom metadata, checksums, and the blobs holding the bytes), journaled
// to mgmt like every store. The BYTES are immutable blobs in the host's R2
// backend, which only hostd talks to (/v1/r2/blob on the HOSTD binding).
//
// The tenant's env.<Binding> is workerd's own r2Bucket binding targeting
// R2Backend below with props {kind: "r2", name}. workerd turns the R2 API
// into HTTP requests (the protocol miniflare serves for wrangler dev):
//   GET, header cf-r2-request: {"method": "head"|"get"|"list", …}
//   PUT, body: JSON (cf-r2-metadata-size bytes) then the value:
//        {"method": "put"|"delete"|"createMultipartUpload"|"uploadPart"|
//                   "completeMultipartUpload"|"abortMultipartUpload", …}
// and expects the object's metadata JSON (cf-r2-metadata-size) followed by
// its bytes, or an error in cf-r2-error. Bodies stream through: a put's
// value goes from workerd to hostd to the backend, a get's from the
// backend to hostd to workerd, never whole in memory.
//
// Facets (Dynamic Workers) can't hold a native binding: their env gets an
// RPC stub to R2FacetProxy (which calls the platform's own native binding
// of the same bucket), wrapped by the facet shim (store-r2-facet.js).
import { WorkerEntrypoint } from "cloudflare:workers";
import { registerStoreKind, storeStub, utf8 } from "tw-store.js";

const MAX_KEY = 1024;
const MAX_META = 8192;
const MAX_VALUE = 5 * 1024 * 1024 * 1024 - 5 * 1024 * 1024;
const MAX_LIST = 1000;
const MIN_PART = 5 * 1024 * 1024;
const MAX_PART_NUMBER = 10000;
const MAX_ENTRIES = 100; // per journal append (the journal takes 128)
const HASHES = [
  ["md5", "MD5"],
  ["sha1", "SHA-1"],
  ["sha256", "SHA-256"],
  ["sha384", "SHA-384"],
  ["sha512", "SHA-512"],
];

const te = new TextEncoder();

// ---- R2 protocol errors (status, v4code, message as R2 / miniflare) ----

class R2Err extends Error {
  constructor(status, v4code, message) {
    super(message);
    this.status = status;
    this.v4code = v4code;
  }
}
const ERR = {
  internal: (m) => new R2Err(500, 10001, m ?? "We encountered an internal error. Please try again."),
  noSuchKey: () => new R2Err(404, 10007, "The specified key does not exist."),
  tooLarge: () => new R2Err(400, 100100, "Your proposed upload exceeds the maximum allowed object size."),
  tooSmall: () => new R2Err(400, 10011, "Your proposed upload is smaller than the minimum allowed object size."),
  metaTooLarge: () => new R2Err(400, 10012, "Your metadata headers exceed the maximum allowed metadata size."),
  badName: () => new R2Err(400, 10020, "The specified object name is not valid."),
  badMaxKeys: () => new R2Err(400, 10022, "MaxKeys params must be positive integer <= 1000."),
  noSuchUpload: () => new R2Err(400, 10024, "The specified multipart upload does not exist."),
  invalidPart: () => new R2Err(400, 10025, "One or more of the specified parts could not be found."),
  invalidArg: (m) => new R2Err(400, 10029, m ?? "Metadata missing or invalid"),
  precondition: () => new R2Err(412, 10031, "At least one of the pre-conditions you specified did not hold."),
  badDigest: (alg) => new R2Err(400, 10037, "The " + alg + " checksum you specified did not match what we received."),
  badRange: () => new R2Err(416, 10039, "The requested range is not satisfiable"),
  badUpload: () => new R2Err(500, 10048, "There was a problem with the multipart upload."),
};

// R2_ENTRY_V is the index journal entry version this tailworks writes.
const R2_ENTRY_V = 1;

function errResponse(e, meta) {
  const headers = { "cf-r2-error": JSON.stringify({ message: e.message, version: 1, v4code: e.v4code }) };
  if (meta) {
    const b = te.encode(JSON.stringify(meta));
    headers["cf-r2-metadata-size"] = String(b.byteLength);
    headers["content-type"] = "application/json";
    return new Response(b, { status: e.status, headers });
  }
  return new Response(null, { status: e.status, headers });
}

function jsonResponse(v) {
  const b = te.encode(JSON.stringify(v));
  return new Response(b, { headers: { "cf-r2-metadata-size": String(b.byteLength), "content-type": "application/json" } });
}

// ---- helpers ----

function hex(buf) {
  return [...new Uint8Array(buf)].map((x) => x.toString(16).padStart(2, "0")).join("");
}
function unhex(s) {
  const out = new Uint8Array(s.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(s.slice(2 * i, 2 * i + 2), 16);
  return out;
}
function b64ToHex(s) {
  const bin = atob(s);
  let o = "";
  for (let i = 0; i < bin.length; i++) o += bin.charCodeAt(i).toString(16).padStart(2, "0");
  return o;
}
function randomHex(n) {
  return hex(crypto.getRandomValues(new Uint8Array(n)));
}
function b64utf8(str) {
  let s = "";
  for (const c of te.encode(str)) s += String.fromCharCode(c);
  return btoa(s);
}
function unb64(s) {
  const bin = atob(s);
  const u = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) u[i] = bin.charCodeAt(i);
  return utf8(u);
}

function checkKey(k) {
  if (typeof k !== "string" || te.encode(k).byteLength > MAX_KEY) throw ERR.badName();
}
function serialisedLength(x) {
  for (let i = 0; i < x.length; i++) if (x.charCodeAt(i) >= 256) return x.length * 2;
  return x.length;
}
function checkCustom(custom) {
  let n = 0;
  for (const [k, v] of Object.entries(custom ?? {})) n += serialisedLength(k) + serialisedLength(v);
  if (n > MAX_META) throw ERR.metaTooLarge();
}
// customFields [{k, v}] → {k: v}
function customOf(fields) {
  const o = {};
  for (const f of fields ?? []) o[String(f.k)] = String(f.v);
  return o;
}

// condOK evaluates an R2Conditional against an index row (or none):
// R2's (and miniflare's) semantics, If-Match / If-None-Match /
// If-Modified-Since / If-Unmodified-Since.
export function condOK(c, row) {
  if (!c) return true;
  if (!row) return c.etagMatches === undefined && c.uploadedAfter === undefined;
  const trunc = c.secondsGranularity ? (ms) => Math.floor(ms / 1000) * 1000 : (ms) => ms;
  const has = (conds, etag, cmp) => conds.some((x) => x.type === "wildcard" || (x.value === etag && (x.type === "strong" || cmp === "weak")));
  const ifMatch = c.etagMatches === undefined || has(c.etagMatches, row.etag, "strong");
  const ifNoneMatch = c.etagDoesNotMatch === undefined || !has(c.etagDoesNotMatch, row.etag, "weak");
  const lm = trunc(row.uploaded);
  const ifModifiedSince = c.uploadedAfter === undefined || trunc(Number(c.uploadedAfter)) < lm || (c.etagDoesNotMatch !== undefined && ifNoneMatch);
  const ifUnmodifiedSince = c.uploadedBefore === undefined || lm < trunc(Number(c.uploadedBefore)) || (c.etagMatches !== undefined && ifMatch);
  return ifMatch && ifNoneMatch && ifModifiedSince && ifUnmodifiedSince;
}

// rangeOf is the {offset, length} a get asks for (undefined: all of it).
export function rangeOf(m, size) {
  if (m.rangeHeader !== undefined) {
    const spec = String(m.rangeHeader).trim();
    if (!spec.startsWith("bytes=")) return undefined;
    const parts = spec.slice(6).split(",");
    if (parts.length !== 1) return undefined;
    const mm = /^\s*(\d*)\s*-\s*(\d*)\s*$/.exec(parts[0]);
    if (!mm || (mm[1] === "" && mm[2] === "")) return undefined;
    let start, end;
    if (mm[1] === "") {
      const n = Number(mm[2]);
      if (n === 0) throw ERR.badRange();
      start = Math.max(size - n, 0);
      end = size - 1;
    } else {
      start = Number(mm[1]);
      end = mm[2] === "" ? size - 1 : Math.min(Number(mm[2]), size - 1);
      if (end < start) return undefined;
    }
    if (start >= size) throw ERR.badRange();
    return { offset: start, length: end - start + 1 };
  }
  if (m.range !== undefined) {
    const num = (v) => (v === undefined || v === null ? undefined : Number(v));
    let offset = num(m.range.offset);
    let length = num(m.range.length);
    let suffix = num(m.range.suffix);
    if (suffix !== undefined) {
      if (suffix <= 0) throw ERR.badRange();
      if (suffix > size) suffix = size;
      offset = size - suffix;
      length = suffix;
    }
    if (offset === undefined) offset = 0;
    if (length === undefined) length = size - offset;
    if (!(offset >= 0) || offset > size || !(length > 0)) throw ERR.badRange();
    if (offset + length > size) length = size - offset;
    return { offset, length };
  }
  return undefined;
}

// rawMeta is an index row as workerd's R2 binding expects it.
function rawMeta(o, range, include) {
  const r = {
    name: o.key,
    version: o.version,
    size: o.size,
    etag: o.etag,
    uploaded: o.uploaded,
    httpFields: !include || include.http ? o.http : {},
    customFields: !include || include.custom ? Object.entries(o.custom).map(([k, v]) => ({ k, v })) : [],
    checksums: {},
    storageClass: "Standard",
  };
  if (range) r.range = range;
  HASHES.forEach(([f], i) => {
    if (o.checksums[f]) r.checksums[i] = o.checksums[f];
  });
  return r;
}

// ---- the index (Store kind "r2") ----

function decodeRow(r) {
  return {
    key: r.key,
    version: r.version,
    size: r.size,
    etag: r.etag,
    uploaded: r.uploaded,
    checksums: JSON.parse(r.checksums),
    http: JSON.parse(r.http),
    custom: JSON.parse(r.custom),
    blobs: JSON.parse(r.blobs),
  };
}
function getRow(s, key) {
  const r = s.sql.exec("SELECT * FROM r2_objects WHERE key = ?", key).toArray()[0];
  return r ? decodeRow(r) : null;
}
function getUpload(s, id, key) {
  const r = s.sql.exec("SELECT * FROM r2_uploads WHERE id = ? AND key = ?", id, key).toArray()[0];
  return r ? { id: r.id, key: r.key, http: JSON.parse(r.http), custom: JSON.parse(r.custom) } : null;
}
function partsOf(s, id) {
  const m = new Map();
  for (const r of s.sql.exec("SELECT n, blob, size, etag, md5 FROM r2_parts WHERE upload = ?", id)) m.set(r.n, r);
  return m;
}
const blobIDs = (row) => (row ? row.blobs.map((b) => b.id) : []);

// fresh: R2 is read-after-write consistent, so a read catches up with the
// journal first (cheap when nothing changed). Concurrent reads share one
// catch-up: a catch-up that started after a read began covers it.
const syncs = new WeakMap();
async function fresh(s) {
  const t = Date.now();
  let x = syncs.get(s);
  if (x && x.at >= t) return x.p;
  if (x) await x.p.catch(() => {});
  x = syncs.get(s);
  if (x && x.at >= t) return x.p;
  const p = s.fresh(0);
  syncs.set(s, { p, at: Date.now() });
  return p;
}

// blobGC deletes blobs nothing references any more (best effort; an
// orphan is only wasted space).
function blobGC(env, waitUntil, bucket, ids) {
  ids = (ids ?? []).filter(Boolean);
  for (let i = 0; i < ids.length; i += 50) {
    const q = new URLSearchParams({ bucket });
    for (const id of ids.slice(i, i + 50)) q.append("id", id);
    const p = env.HOSTD.fetch("http://hostd/v1/r2/blob?" + q, { method: "DELETE" })
      .then((r) => {
        if (!r.ok) console.warn("tw r2 " + bucket + ": blob delete answered " + r.status);
      })
      .catch((e) => console.warn("tw r2 " + bucket + ": blob delete: " + (e?.message ?? e)));
    waitUntil(p);
  }
}

registerStoreKind("r2", {
  init(s) {
    s.sql.exec(`CREATE TABLE IF NOT EXISTS r2_objects (
      key TEXT PRIMARY KEY, version TEXT NOT NULL, size INTEGER NOT NULL, etag TEXT NOT NULL,
      uploaded INTEGER NOT NULL, checksums TEXT NOT NULL, http TEXT NOT NULL, custom TEXT NOT NULL,
      blobs TEXT NOT NULL)`);
    s.sql.exec(`CREATE TABLE IF NOT EXISTS r2_uploads (
      id TEXT PRIMARY KEY, key TEXT NOT NULL, http TEXT NOT NULL, custom TEXT NOT NULL, created INTEGER NOT NULL)`);
    s.sql.exec(`CREATE TABLE IF NOT EXISTS r2_parts (
      upload TEXT NOT NULL, n INTEGER NOT NULL, blob TEXT NOT NULL, size INTEGER NOT NULL,
      etag TEXT NOT NULL, md5 TEXT NOT NULL, PRIMARY KEY (upload, n))`);
  },
  // Journal entries (JSON), with their compaction keys. Every entry
  // carries v (ADR-041 §4): R2_ENTRY_V is what this tailworks
  // writes; an entry without v was written before versioning and is v1.
  // A newer v (or an unknown op) was written by a newer tailworks: apply throws,
  // so the store refuses to sync instead of silently dropping writes.
  //   {o:"put", k, row, up?}   ckey "o:"+k  (up: the multipart upload it completes)
  //   {o:"del", k}             ckey "o:"+k
  //   {o:"mpu", id, k, http, custom, created}  ckey "u:"+id
  //   {o:"part", id, n, blob, size, etag, md5} ckey "p:"+id+":"+n
  //   {o:"mpx", id}            ckey "u:"+id  (upload completed or aborted)
  apply(s, data) {
    const e = JSON.parse(utf8(data));
    const v = e.v ?? 1;
    if (v > R2_ENTRY_V) throw new Error(`tw r2: index entry v${v} was written by a newer tailworks (this one reads up to v${R2_ENTRY_V}); upgrade tw-hostd`);
    switch (e.o) {
      case "put": {
        const r = e.row;
        s.sql.exec(
          "INSERT OR REPLACE INTO r2_objects (key, version, size, etag, uploaded, checksums, http, custom, blobs) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
          e.k, r.version, r.size, r.etag, r.uploaded, JSON.stringify(r.checksums), JSON.stringify(r.http), JSON.stringify(r.custom), JSON.stringify(r.blobs),
        );
        if (e.up) {
          s.sql.exec("DELETE FROM r2_uploads WHERE id = ?", e.up);
          s.sql.exec("DELETE FROM r2_parts WHERE upload = ?", e.up);
        }
        break;
      }
      case "del":
        s.sql.exec("DELETE FROM r2_objects WHERE key = ?", e.k);
        break;
      case "mpu":
        s.sql.exec("INSERT OR REPLACE INTO r2_uploads (id, key, http, custom, created) VALUES (?, ?, ?, ?, ?)", e.id, e.k, JSON.stringify(e.http), JSON.stringify(e.custom), e.created);
        break;
      case "part":
        s.sql.exec("INSERT OR REPLACE INTO r2_parts (upload, n, blob, size, etag, md5) VALUES (?, ?, ?, ?, ?, ?)", e.id, e.n, e.blob, e.size, e.etag, e.md5);
        break;
      case "mpx":
        s.sql.exec("DELETE FROM r2_uploads WHERE id = ?", e.id);
        s.sql.exec("DELETE FROM r2_parts WHERE upload = ?", e.id);
        break;
      default:
        throw new Error(`tw r2: unknown index entry op ${JSON.stringify(e.o)} (v${v}): written by a newer tailworks? upgrade tw-hostd`);
    }
  },
  // reset: the bucket's store was deleted (or deleted and created again).
  // The blobs this index pointed to belong to no live bucket: delete them.
  reset(s) {
    const ids = [];
    for (const r of s.sql.exec("SELECT blobs FROM r2_objects")) ids.push(...JSON.parse(r.blobs).map((b) => b.id));
    for (const r of s.sql.exec("SELECT blob FROM r2_parts")) ids.push(r.blob);
    s.sql.exec("DELETE FROM r2_objects");
    s.sql.exec("DELETE FROM r2_uploads");
    s.sql.exec("DELETE FROM r2_parts");
    blobGC(s.env, (p) => s.ctx.waitUntil(p), s.name, ids);
  },
  ops: {
    async head(s, key) {
      await fresh(s);
      return getRow(s, key);
    },

    // put commits row for key if onlyIf holds (checked against the index at
    // append time, again after a conflict). Returns {failed} or {garbage}.
    async put(s, key, row, onlyIf) {
      let res;
      await s.commit(() => {
        const old = getRow(s, key);
        if (!condOK(onlyIf, old)) {
          res = { failed: true };
          return [];
        }
        res = { garbage: blobIDs(old) };
        return [{ ckey: "o:" + key, data: JSON.stringify({ v: R2_ENTRY_V, o: "put", k: key, row }) }];
      });
      return res;
    },

    async del(s, keys) {
      const garbage = [];
      const uniq = [...new Set(keys)];
      for (let i = 0; i < uniq.length; i += MAX_ENTRIES) {
        const chunk = uniq.slice(i, i + MAX_ENTRIES);
        let g = [];
        await s.commit(() => {
          g = [];
          const es = [];
          for (const k of chunk) {
            const old = getRow(s, k);
            if (!old) continue;
            g.push(...blobIDs(old));
            es.push({ ckey: "o:" + k, data: JSON.stringify({ v: R2_ENTRY_V, o: "del", k }) });
          }
          return es;
        });
        garbage.push(...g);
      }
      return garbage;
    },

    async list(s, o) {
      await fresh(s);
      const prefix = o.prefix ?? "";
      let limit = o.limit ?? MAX_LIST;
      const include = { http: false, custom: false };
      for (const i of o.include ?? []) {
        if (i === 0 || i === "httpMetadata") include.http = true;
        if (i === 1 || i === "customMetadata") include.custom = true;
      }
      // limits.md#r2-list-include
      if (include.http || include.custom) limit = Math.min(limit, 100);
      let startAfter = o.startAfter;
      if (o.cursor) {
        const c = unb64(o.cursor);
        if (startAfter === undefined || c > startAfter) startAfter = c;
      }
      const sa = startAfter ?? null;
      const delim = o.delimiter || undefined;
      const objects = [];
      const delimitedPrefixes = [];
      let next;
      if (delim !== undefined) {
        const rows = s.sql
          .exec(
            `SELECT max(key) AS last_key,
               iif(instr(substr(key, length(?) + 1), ?),
                   'dlp:' || substr(key, 1, length(?) + instr(substr(key, length(?) + 1), ?) + length(?) - 1),
                   'key:' || key) AS dk,
               key, version, size, etag, uploaded, checksums, http, custom, blobs
             FROM r2_objects
             WHERE substr(key, 1, length(?)) = ? AND (? IS NULL OR key > ?)
             GROUP BY dk ORDER BY last_key LIMIT ?`,
            prefix, delim, prefix, prefix, delim, delim, prefix, prefix, sa, sa, limit + 1,
          )
          .toArray();
        const more = rows.length === limit + 1;
        rows.splice(limit, 1);
        for (const r of rows) {
          if (r.dk.startsWith("dlp:")) delimitedPrefixes.push(r.dk.slice(4));
          else objects.push(decodeRow({ ...r, key: r.last_key }));
        }
        if (more) next = rows[limit - 1].last_key;
      } else {
        const rows = s.sql
          .exec("SELECT * FROM r2_objects WHERE substr(key, 1, length(?)) = ? AND (? IS NULL OR key > ?) ORDER BY key LIMIT ?", prefix, prefix, sa, sa, limit + 1)
          .toArray();
        const more = rows.length === limit + 1;
        rows.splice(limit, 1);
        for (const r of rows) objects.push(decodeRow(r));
        if (more) next = rows[limit - 1].key;
      }
      return { objects, include, truncated: next !== undefined, cursor: next === undefined ? undefined : b64utf8(next), delimitedPrefixes };
    },

    async mpuCreate(s, key, http, custom) {
      const id = randomHex(24);
      await s.commit([{ ckey: "u:" + id, data: JSON.stringify({ v: R2_ENTRY_V, o: "mpu", id, k: key, http, custom, created: Date.now() }) }]);
      return id;
    },

    async mpuCheck(s, id, key) {
      await fresh(s);
      return getUpload(s, id, key) !== null;
    },

    async mpuPart(s, id, key, part) {
      let res;
      await s.commit(() => {
        if (!getUpload(s, id, key)) {
          res = { noUpload: true };
          return [];
        }
        const old = partsOf(s, id).get(part.n);
        res = { garbage: old ? [old.blob] : [] };
        return [{ ckey: "p:" + id + ":" + part.n, data: JSON.stringify({ v: R2_ENTRY_V, o: "part", id, ...part }) }];
      });
      return res;
    },

    async mpuComplete(s, id, key, sel) {
      for (let attempt = 0; attempt < 4; attempt++) {
        await fresh(s);
        const up = getUpload(s, id, key);
        if (!up) return { err: "noSuchUpload" };
        const pick = () => {
          const rows = partsOf(s, id);
          const seen = new Set();
          const chosen = [];
          for (const p of sel) {
            const n = Number(p.part);
            if (seen.has(n)) return { err: "invalidPart" };
            seen.add(n);
            const r = rows.get(n);
            if (!r || r.etag !== p.etag) return { err: "invalidPart" };
            chosen.push(r);
          }
          chosen.sort((a, b) => a.n - b.n);
          return { rows, chosen };
        };
        const first = pick();
        if (first.err) return first;
        const { chosen } = first;
        let partSize;
        for (const r of chosen.slice(0, -1)) {
          if (r.size < MIN_PART) return { err: "tooSmall" };
          partSize ??= r.size;
          if (r.size !== partSize) return { err: "badUpload" };
        }
        if (partSize !== undefined && chosen[chosen.length - 1].size > partSize) return { err: "badUpload" };
        const md5s = new Uint8Array(chosen.length * 16);
        chosen.forEach((r, i) => md5s.set(unhex(r.md5), i * 16));
        const etag = hex(await crypto.subtle.digest("MD5", md5s)) + "-" + chosen.length;
        const snap = JSON.stringify(chosen.map((r) => [r.n, r.blob]));
        const row = {
          version: randomHex(16),
          size: chosen.reduce((a, r) => a + r.size, 0),
          etag,
          uploaded: Date.now(),
          checksums: {},
          http: up.http,
          custom: up.custom,
          blobs: chosen.map((r) => ({ id: r.blob, size: r.size })),
        };
        let res;
        await s.commit(() => {
          if (!getUpload(s, id, key)) {
            res = { err: "noSuchUpload" };
            return [];
          }
          const again = pick();
          if (again.err || JSON.stringify(again.chosen.map((r) => [r.n, r.blob])) !== snap) {
            res = { retry: true }; // a part changed under us: recompute
            return [];
          }
          const used = new Set(again.chosen.map((r) => r.blob));
          const unused = [...again.rows.values()].filter((r) => !used.has(r.blob)).map((r) => r.blob);
          res = { row, garbage: [...blobIDs(getRow(s, key)), ...unused] };
          return [
            { ckey: "o:" + key, data: JSON.stringify({ v: R2_ENTRY_V, o: "put", k: key, row, up: id }) },
            { ckey: "u:" + id, data: JSON.stringify({ v: R2_ENTRY_V, o: "mpx", id }) },
          ];
        });
        if (!res.retry) return res;
      }
      return { err: "internal" };
    },

    async mpuAbort(s, id, key) {
      let garbage = [];
      await s.commit(() => {
        garbage = [];
        if (!getUpload(s, id, key)) return [];
        garbage = [...partsOf(s, id).values()].map((r) => r.blob);
        return [{ ckey: "u:" + id, data: JSON.stringify({ v: R2_ENTRY_V, o: "mpx", id }) }];
      });
      return garbage;
    },
  },
});

// ---- the R2 protocol (workerd's r2Bucket binding → here) ----

// splitBody reads exactly the metadata prefix (n bytes) off a PUT body
// with a BYOB reader, so nothing of the value is read into JS: the rest
// of the body can then be piped natively (no per-chunk JS buffers).
async function splitBody(body, n) {
  let reader;
  try {
    reader = body.getReader({ mode: "byob" });
  } catch {
    reader = null;
  }
  const meta = new Uint8Array(n);
  let got = 0;
  if (reader) {
    let buf = meta.buffer;
    while (got < n) {
      const { done, value } = await reader.read(new Uint8Array(buf, got, n - got));
      if (done || !value) throw ERR.invalidArg();
      buf = value.buffer;
      got += value.byteLength;
    }
    reader.releaseLock();
    return { meta: JSON.parse(utf8(new Uint8Array(buf, 0, n))), body };
  }
  // Not a byte stream: read chunks, keep what overshoots.
  reader = body.getReader();
  let rest = null;
  while (got < n) {
    const { done, value } = await reader.read();
    if (done) throw ERR.invalidArg();
    const u = value instanceof Uint8Array ? value : new Uint8Array(value);
    const need = n - got;
    meta.set(u.subarray(0, Math.min(need, u.byteLength)), got);
    if (u.byteLength > need) rest = u.subarray(need);
    got += Math.min(need, u.byteLength);
  }
  reader.releaseLock();
  if (!rest) return { meta: JSON.parse(utf8(meta)), body };
  const { readable, writable } = new TransformStream();
  const w = writable.getWriter();
  w.write(rest).then(() => w.releaseLock()).then(() => body.pipeTo(writable)).catch(() => {});
  return { meta: JSON.parse(utf8(meta)), body: readable };
}

// drain reads what is left of a request body and drops it (workerd's
// binding treats an unread request body as a lost connection).
async function drain(body) {
  try {
    await body.pipeTo(new WritableStream());
  } catch {
    /* gone */
  }
}

// valueStream is the rest of a PUT body as a stream of known length,
// piped natively.
function valueStream(body, size, waitUntil) {
  const { readable, writable } = new FixedLengthStream(size);
  waitUntil(body.pipeTo(writable).catch(() => {}));
  return readable;
}

export class R2Backend extends WorkerEntrypoint {
  get #bucket() {
    return this.ctx.props.name;
  }
  #store() {
    return storeStub(this.ctx.exports, "r2", this.ctx.props.name);
  }
  #wait = (p) => this.ctx.waitUntil(p);
  #gc(ids) {
    blobGC(this.env, this.#wait, this.#bucket, ids);
  }

  async fetch(req) {
    try {
      if (req.method === "GET") {
        let m;
        try {
          m = JSON.parse(req.headers.get("cf-r2-request"));
        } catch {
          throw ERR.invalidArg();
        }
        switch (m?.method) {
          case "head":
            return await this.#head(m);
          case "get":
            return await this.#get(m);
          case "list":
            return await this.#list(m);
        }
        throw ERR.invalidArg("unknown R2 operation " + String(m?.method));
      }
      if (req.method === "PUT") {
        const metaSize = parseInt(req.headers.get("cf-r2-metadata-size") ?? "NaN", 10);
        if (!(metaSize > 0) || !req.body) throw ERR.invalidArg();
        const { meta, body } = await splitBody(req.body, metaSize);
        const len = parseInt(req.headers.get("content-length") ?? "NaN", 10);
        const valueSize = Number.isNaN(len) ? NaN : len - metaSize;
        switch (meta.method) {
          case "put":
            return await this.#put(meta, body, valueSize);
          case "uploadPart":
            return await this.#uploadPart(meta, body, valueSize);
        }
        await drain(body);
        switch (meta.method) {
          case "delete":
            return await this.#delete(meta);
          case "createMultipartUpload":
            return await this.#mpuCreate(meta);
          case "completeMultipartUpload":
            return await this.#mpuComplete(meta);
          case "abortMultipartUpload":
            return await this.#mpuAbort(meta);
        }
        throw ERR.invalidArg("unknown R2 operation " + String(meta.method));
      }
      throw ERR.invalidArg("unexpected method " + req.method);
    } catch (e) {
      if (e instanceof R2Err) return errResponse(e, e.meta);
      const msg = String(e?.message ?? e);
      console.error("tw r2 " + this.#bucket + ": " + msg);
      return errResponse(ERR.internal("tw r2: " + msg));
    }
  }

  async #head(m) {
    checkKey(m.object);
    const row = await this.#store().op("head", [m.object]);
    if (!row) throw ERR.noSuchKey();
    return jsonResponse(rawMeta(row, { offset: 0, length: row.size }));
  }

  async #get(m, retried) {
    checkKey(m.object);
    const row = await this.#store().op("head", [m.object]);
    if (!row) throw ERR.noSuchKey();
    if (!condOK(m.onlyIf, row)) {
      const e = ERR.precondition();
      e.meta = rawMeta(row, { offset: 0, length: row.size });
      throw e;
    }
    const range = rangeOf(m, row.size) ?? { offset: 0, length: row.size };
    // The blob segments covering the range.
    const segs = [];
    let pos = 0;
    const end = range.offset + range.length;
    for (const b of row.blobs) {
      const bs = pos;
      const be = pos + b.size;
      pos = be;
      if (be <= range.offset || bs >= end) continue;
      const off = Math.max(range.offset, bs) - bs;
      segs.push({ id: b.id, off, len: Math.min(end, be) - bs - off });
    }
    const open = (sg) => {
      const q = new URLSearchParams({ bucket: this.#bucket, id: sg.id, off: String(sg.off), len: String(sg.len) });
      return this.env.HOSTD.fetch("http://hostd/v1/r2/blob?" + q);
    };
    // Open the first segment before answering, so a missing blob is an
    // error, not a truncated body. A blob deleted between our index read
    // and here was overwritten: read the index again, once.
    let first = null;
    if (segs.length) {
      first = await open(segs[0]);
      if (!first.ok) {
        const body = await first.json().catch(() => ({}));
        if (first.status === 404 && !retried) return this.#get(m, true);
        throw ERR.internal("tw r2: " + (body.code ?? "r2_backend_unavailable") + ": " + (body.message ?? "blob read answered " + first.status));
      }
    }
    const meta = te.encode(JSON.stringify(rawMeta(row, range)));
    const { readable, writable } = new FixedLengthStream(meta.byteLength + range.length);
    this.#wait(
      (async () => {
        try {
          const w = writable.getWriter();
          await w.write(meta);
          w.releaseLock();
          for (let i = 0; i < segs.length; i++) {
            const r = i === 0 ? first : await open(segs[i]);
            if (!r.ok) throw new Error("blob read answered " + r.status);
            await r.body.pipeTo(writable, { preventClose: true });
          }
          await writable.close();
        } catch (e) {
          console.error("tw r2 " + this.#bucket + ": get " + m.object + ": " + (e?.message ?? e));
          await writable.abort(e).catch(() => {});
        }
      })(),
    );
    return new Response(readable, {
      headers: { "cf-r2-metadata-size": String(meta.byteLength), "content-type": "application/json" },
    });
  }

  async #list(m) {
    const limit = m.limit;
    if (limit !== undefined && !(limit >= 1 && limit <= MAX_LIST)) throw ERR.badMaxKeys();
    const r = await this.#store().op("list", [{ prefix: m.prefix, limit, cursor: m.cursor, delimiter: m.delimiter, startAfter: m.startAfter, include: m.include }]);
    return jsonResponse({
      objects: r.objects.map((o) => rawMeta(o, undefined, r.include)),
      truncated: r.truncated,
      cursor: r.cursor,
      delimitedPrefixes: r.delimitedPrefixes,
    });
  }

  // #putBlob streams a value to hostd; returns {id, size, md5, sha…}.
  async #putBlob(body, size, hashes) {
    const q = new URLSearchParams({ bucket: this.#bucket });
    for (const h of hashes) q.append("hash", h);
    const r = await this.env.HOSTD.fetch("http://hostd/v1/r2/blob?" + q, {
      method: "PUT",
      body: valueStream(body, size, this.#wait),
    });
    const j = await r.json().catch(() => ({}));
    if (!r.ok) {
      if (j.code === "r2_too_large") throw ERR.tooLarge();
      throw ERR.internal("tw r2: " + (j.code ?? "r2_backend_unavailable") + ": " + (j.message ?? "blob write answered " + r.status));
    }
    return j;
  }

  async #put(m, body, size) {
    const discard = () => drain(body);
    let custom;
    try {
      checkKey(m.object);
      custom = customOf(m.customFields);
      checkCustom(custom);
      if (!(size >= 0)) throw ERR.invalidArg("tw r2: a put body needs a known length");
      if (size > MAX_VALUE) throw ERR.tooLarge();
      // Fail fast before moving bytes; the append checks again.
      if (m.onlyIf && !condOK(m.onlyIf, await this.#store().op("head", [m.object]))) throw ERR.precondition();
    } catch (e) {
      await discard();
      throw e;
    }
    const want = HASHES.filter(([f]) => f !== "md5" && m[f] !== undefined).map(([, a]) => a);
    const blob = await this.#putBlob(body, size, want);
    const checksums = { md5: blob.md5 };
    try {
      if (m.md5 !== undefined && b64ToHex(m.md5) !== blob.md5) throw ERR.badDigest("MD5");
      for (const [f, a] of HASHES) {
        if (f === "md5" || m[f] === undefined) continue;
        if (String(m[f]).toLowerCase() !== blob[f]) throw ERR.badDigest(a);
        checksums[f] = blob[f];
      }
      const row = {
        version: randomHex(16),
        size: blob.size,
        etag: blob.md5,
        uploaded: Date.now(),
        checksums,
        http: m.httpFields ?? {},
        custom,
        blobs: [{ id: blob.id, size: blob.size }],
      };
      const res = await this.#store().op("put", [m.object, row, m.onlyIf]);
      if (res.failed) throw ERR.precondition();
      this.#gc(res.garbage);
      return jsonResponse(rawMeta({ key: m.object, ...row }));
    } catch (e) {
      this.#gc([blob.id]);
      throw e;
    }
  }

  async #delete(m) {
    const keys = m.objects ?? [m.object];
    for (const k of keys) checkKey(k);
    const garbage = await this.#store().op("del", [keys]);
    this.#gc(garbage);
    return new Response(null);
  }

  async #mpuCreate(m) {
    checkKey(m.object);
    const custom = customOf(m.customFields);
    checkCustom(custom);
    const id = await this.#store().op("mpuCreate", [m.object, m.httpFields ?? {}, custom]);
    return jsonResponse({ uploadId: id });
  }

  async #uploadPart(m, body, size) {
    try {
      checkKey(m.object);
      const n = Number(m.partNumber);
      if (!Number.isInteger(n) || n < 1 || n > MAX_PART_NUMBER) throw ERR.invalidArg("part number must be 1 to 10000");
      if (!(size >= 0)) throw ERR.invalidArg("tw r2: a part body needs a known length");
      if (size > MAX_VALUE) throw ERR.tooLarge();
      if (!(await this.#store().op("mpuCheck", [m.uploadId, m.object]))) throw ERR.noSuchUpload();
    } catch (e) {
      await drain(body);
      throw e;
    }
    const blob = await this.#putBlob(body, size, []);
    try {
      const part = { n: Number(m.partNumber), blob: blob.id, size: blob.size, etag: blob.md5, md5: blob.md5 };
      const res = await this.#store().op("mpuPart", [m.uploadId, m.object, part]);
      if (res.noUpload) throw ERR.noSuchUpload();
      this.#gc(res.garbage);
      return jsonResponse({ etag: part.etag });
    } catch (e) {
      this.#gc([blob.id]);
      throw e;
    }
  }

  async #mpuComplete(m) {
    checkKey(m.object);
    const res = await this.#store().op("mpuComplete", [m.uploadId, m.object, m.parts ?? []]);
    if (res.err) throw (ERR[res.err] ?? ERR.internal)();
    this.#gc(res.garbage);
    return jsonResponse(rawMeta({ key: m.object, ...res.row }));
  }

  async #mpuAbort(m) {
    checkKey(m.object);
    this.#gc(await this.#store().op("mpuAbort", [m.uploadId, m.object]));
    return new Response(null);
  }
}

// ---- facets ----

// R2FacetProxy is what a facet's env.<Binding> reaches (wrapped by the
// facet shim): each call goes to the platform's native binding of the
// same bucket, and results come back as plain objects (an R2ObjectBody
// can't cross RPC; its body stream can).
function toPlain(o, withBody) {
  if (o == null) return null;
  const cs = {};
  for (const [f] of HASHES) if (o.checksums?.[f]) cs[f] = o.checksums[f];
  const p = {
    key: o.key,
    version: o.version,
    size: o.size,
    etag: o.etag,
    httpEtag: o.httpEtag,
    uploaded: o.uploaded,
    httpMetadata: { ...(o.httpMetadata ?? {}) },
    customMetadata: { ...(o.customMetadata ?? {}) },
    range: o.range,
    checksums: cs,
    storageClass: o.storageClass,
  };
  if (withBody && "body" in o) p.body = o.body;
  return p;
}

export class R2FacetProxy extends WorkerEntrypoint {
  #b() {
    const b = this.env["__TW_STORE_" + this.ctx.props.binding];
    if (!b) throw new Error("[bad_request] no R2 binding " + this.ctx.props.binding);
    return b;
  }
  async head(key) {
    return toPlain(await this.#b().head(key), false);
  }
  async get(key, options) {
    return toPlain(await this.#b().get(key, options), true);
  }
  async put(key, value, options) {
    return toPlain(await this.#b().put(key, value, options), false);
  }
  async delete(keys) {
    await this.#b().delete(keys);
  }
  async list(options) {
    const r = await this.#b().list(options);
    return { objects: r.objects.map((o) => toPlain(o, false)), truncated: r.truncated, cursor: r.cursor, delimitedPrefixes: r.delimitedPrefixes };
  }
  async createMultipartUpload(key, options) {
    const u = await this.#b().createMultipartUpload(key, options);
    return { key: u.key, uploadId: u.uploadId };
  }
  async uploadPart(key, uploadId, partNumber, value, options) {
    const p = await this.#b().resumeMultipartUpload(key, uploadId).uploadPart(partNumber, value, options);
    return { partNumber: p.partNumber, etag: p.etag };
  }
  async completeMultipartUpload(key, uploadId, parts) {
    return toPlain(await this.#b().resumeMultipartUpload(key, uploadId).complete(parts), false);
  }
  async abortMultipartUpload(key, uploadId) {
    await this.#b().resumeMultipartUpload(key, uploadId).abort();
  }
}
