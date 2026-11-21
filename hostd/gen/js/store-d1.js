// tailworks D1 (TRUSTED; runs in the platform worker). ADR-038.
//
// A D1 database is a platform store of kind "d1" (tw-store.js): one
// platform-owned SQLite Durable Object per database, whose SQL namespace
// holds only the tenant's tables. Tenant code gets workerd's own D1 API
// (the `wrapped` binding "cloudflare-internal:d1-api"); its inner fetcher
// is D1Backend below, with props {kind, name} fixed at deploy. D1Backend
// speaks upstream's wire protocol (POST /query and /execute, JSON
// statements, the x-cf-d1-session-commit-token header) and calls the
// store object, which runs the statements.
//
// Reads run on the object's local copy. Writes are journal-first like
// every store: a request that writes is first run in a transaction that
// is rolled back (a dry run: SQL errors, limits, "did it write at all"),
// then appended to mgmt's journal as one entry (the statements and their
// parameters), then applied in one transaction, so local state is always a
// replay of the journal and an acknowledged write is in mgmt. A batch is
// one entry and one transaction: atomic.
//
// Sessions: a bookmark is the journal position (seq) plus the store's
// incarnation. A read with a bookmark is served once the local copy has
// applied at least that position; "first-primary" catches up first.
import { WorkerEntrypoint } from "cloudflare:workers";
import { registerStoreKind, storeStub, utf8, toBytes } from "tw-store.js";

// Limits, checked before SQLite sees a statement (statement size,
// parameters, value sizes, statements per request) and before a response
// leaves the store (result size). D1_MAX_ENTRY_BYTES is mgmt's journal
// entry limit: one write request, encoded.
export const D1_LIMITS = Object.freeze({
  sqlBytes: 100_000,
  params: 100,
  valueBytes: 2_000_000,
  statements: 1000,
  resultBytes: 16 << 20,
  databaseBytes: 1 << 30,
  entryBytes: 2 << 20,
});

// A read without a session (or "first-unconstrained") catches up with the
// journal when its last sync is older than this. Only another replica of
// the workload writes behind this object's back; use a session bookmark
// or "first-primary" for read-your-writes across replicas.
const READ_FRESH_MS = 1000;

const SESSION_HEADER = "x-cf-d1-session-commit-token";
const te = new TextEncoder();

class D1Fail extends Error {
  constructor(message, index = 0) {
    super(message);
    this.index = index;
  }
}
const ROLLBACK = Symbol("rollback");

function b64(u8) {
  let s = "";
  for (let i = 0; i < u8.length; i += 0x8000) s += String.fromCharCode.apply(null, u8.subarray(i, i + 0x8000));
  return btoa(s);
}
function unb64(s) {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

// param turns one wire parameter into what ctx.storage.sql binds: null,
// a number, a string, or bytes (upstream's bind() sends ArrayBuffers and
// typed arrays as arrays of byte values).
function param(v, i) {
  if (v === null || v === undefined) return null;
  switch (typeof v) {
    case "number":
      return v;
    case "boolean":
      return v ? 1 : 0;
    case "string":
      if (v.length > D1_LIMITS.valueBytes || te.encode(v).length > D1_LIMITS.valueBytes) throw new D1Fail(`parameter ${i + 1} is over ${D1_LIMITS.valueBytes} bytes`);
      return v;
  }
  if (Array.isArray(v) && v.every((b) => typeof b === "number" && b >= 0 && b < 256 && Number.isInteger(b))) {
    if (v.length > D1_LIMITS.valueBytes) throw new D1Fail(`parameter ${i + 1} is over ${D1_LIMITS.valueBytes} bytes`);
    return Uint8Array.from(v);
  }
  throw new D1Fail(`D1_TYPE_ERROR: Type '${typeof v}' not supported for parameter ${i + 1}`);
}

// stripLead drops leading whitespace and comments.
function stripLead(sql) {
  let s = sql;
  for (;;) {
    s = s.replace(/^\s+/, "");
    if (s.startsWith("--")) {
      const n = s.indexOf("\n");
      s = n < 0 ? "" : s.slice(n + 1);
    } else if (s.startsWith("/*")) {
      const n = s.indexOf("*/");
      s = n < 0 ? "" : s.slice(n + 2);
    } else return s;
  }
}

// norm checks one statement against the limits and converts its
// parameters. An empty statement (blank or only comments) is a no-op.
function norm(q, i) {
  if (!q || typeof q !== "object" || typeof q.sql !== "string") throw new D1Fail("each statement needs sql", i);
  const sql = q.sql;
  if (sql.length > D1_LIMITS.sqlBytes || te.encode(sql).length > D1_LIMITS.sqlBytes) throw new D1Fail(`statement too long: over ${D1_LIMITS.sqlBytes} bytes (SQLITE_TOOBIG)`, i);
  const ps = q.params ?? [];
  if (!Array.isArray(ps)) throw new D1Fail("params must be an array", i);
  if (ps.length > D1_LIMITS.params) throw new D1Fail(`too many SQL variables: ${ps.length} (at most ${D1_LIMITS.params})`, i);
  let params;
  try {
    params = ps.map(param);
  } catch (e) {
    throw new D1Fail(e.message, i);
  }
  return { sql, params, empty: stripLead(sql) === "" };
}

// likelyWrite: whether a statement can write, from its first keyword. A
// statement classified as a read still runs in a transaction that checks
// it wrote nothing (rowsWritten, total_changes()); this only saves the
// write path's dry run for obvious reads.
function likelyWrite(sql) {
  const s = stripLead(sql);
  const kw = (/^[A-Za-z]+/.exec(s)?.[0] ?? "").toUpperCase();
  switch (kw) {
    case "SELECT":
    case "VALUES":
    case "EXPLAIN":
      return false;
    case "PRAGMA":
      return s.includes("=");
    case "WITH":
      return /\b(insert|update|delete|replace)\b/i.test(s);
  }
  return true;
}

function cell(v) {
  if (v instanceof ArrayBuffer) return Array.from(new Uint8Array(v)); // D1 returns BLOBs as arrays of bytes
  return v;
}

const ZERO_META = Object.freeze({ served_by: "tw", served_by_primary: true, duration: 0, changes: 0, last_row_id: 0, changed_db: false, size_after: 0, rows_read: 0, rows_written: 0 });

// execAll runs the statements in the current transaction. It throws
// D1Fail (with the statement's index) on an SQL error or a limit; the
// caller's transaction rolls back. collect=false skips building results
// (replay of the journal).
function execAll(s, stmts, format, collect = true) {
  const sql = s.sql;
  const results = [];
  let wrote = false;
  let bytes = 0;
  for (let i = 0; i < stmts.length; i++) {
    const st = stmts[i];
    if (st.empty) {
      results.push({ success: true, results: format === "ROWS_AND_COLUMNS" ? { columns: [], rows: [] } : [], meta: { ...ZERO_META } });
      continue;
    }
    const t0 = performance.now();
    const before = sql.exec("SELECT total_changes() AS c").one().c;
    let cur;
    const rows = [];
    try {
      cur = sql.exec(st.sql, ...st.params);
      for (const r of cur.raw()) {
        if (!collect) continue;
        const row = r.map(cell);
        bytes += JSON.stringify(row).length;
        if (bytes > D1_LIMITS.resultBytes) throw new D1Fail(`result too large: over ${D1_LIMITS.resultBytes} bytes; select fewer rows or columns`, i);
        rows.push(row);
      }
    } catch (e) {
      if (e instanceof D1Fail) throw e;
      throw new D1Fail(String(e?.message ?? e), i);
    }
    const a = sql.exec("SELECT total_changes() AS c, last_insert_rowid() AS l").one();
    const changes = a.c - before;
    const w = cur.rowsWritten > 0 || changes > 0;
    wrote ||= w;
    const size = sql.databaseSize;
    if (w && size > D1_LIMITS.databaseBytes) throw new D1Fail(`database full: ${size} bytes (at most ${D1_LIMITS.databaseBytes})`, i);
    if (!collect) continue;
    const columns = cur.columnNames;
    results.push({
      success: true,
      results: format === "ROWS_AND_COLUMNS" ? { columns, rows } : rows.map((r) => Object.fromEntries(columns.map((c, j) => [c, r[j]]))),
      meta: {
        served_by: "tw",
        served_by_primary: true,
        duration: performance.now() - t0,
        changes,
        last_row_id: a.l,
        changed_db: w,
        size_after: size,
        rows_read: cur.rowsRead,
        rows_written: cur.rowsWritten,
      },
    });
  }
  return { results, wrote };
}

// trial runs the statements in a transaction and keeps the effect only if
// nothing was written (a read). Returns {results, wrote} or {error, index}.
function trial(s, stmts, format) {
  let out;
  try {
    s.ctx.storage.transactionSync(() => {
      out = execAll(s, stmts, format);
      if (out.wrote) throw ROLLBACK;
    });
  } catch (e) {
    if (e === ROLLBACK) return out;
    if (e instanceof D1Fail) return { error: e.message, index: e.index };
    throw e;
  }
  return out;
}

// The journal entry of a write: the request's id (to hand the applied
// results back to the waiting caller) and its statements.
function encodeEntry(id, stmts) {
  const q = stmts.map((st) => ({
    s: st.sql,
    p: st.params.map((v) => (v instanceof Uint8Array ? { $b: b64(v) } : v)),
  }));
  return te.encode(JSON.stringify({ v: D1_ENTRY_V, id, q }));
}
// D1_ENTRY_V is the journal entry version this tailworks writes
// (ADR-041 §4); every D1 entry has carried v since the
// first release, and a missing v reads as v1.
const D1_ENTRY_V = 1;
// D1FormatError: an entry written by a newer tailworks. Like any entry
// that fails to apply, it stops the replay (a skipped write would
// silently lose data).
class D1FormatError extends Error {}
export function decodeEntry(data) {
  // Not norm(): an entry was checked when it was written, and a later
  // change of the limits must not make the journal unreplayable.
  const e = JSON.parse(utf8(data));
  const v = e.v ?? 1;
  if (v > D1_ENTRY_V) throw new D1FormatError(`tw d1: journal entry v${v} was written by a newer tailworks (this one reads up to v${D1_ENTRY_V}); upgrade tw-hostd`);
  const stmts = e.q.map((q) => ({
    sql: q.s,
    params: (q.p ?? []).map((v) => (v && typeof v === "object" && typeof v.$b === "string" ? unb64(v.$b) : v)),
    empty: stripLead(q.s) === "",
  }));
  return { id: e.id, stmts };
}

async function write(s, stmts, format) {
  const id = crypto.randomUUID();
  let last = null;
  s.d1Want.set(id, format);
  try {
    await s.commit(() => {
      // Re-run after a conflict: the dry run sees the caught-up state.
      const t = trial(s, stmts, format);
      last = t;
      if (t.error !== undefined) throw new D1Fail(t.error, t.index);
      if (!t.wrote) return []; // nothing to write after all (e.g. UPDATE … WHERE matched nothing)
      const data = encodeEntry(id, stmts);
      if (data.length > D1_LIMITS.entryBytes) throw new D1Fail(`write too large: ${data.length} bytes encoded (one write or batch is at most ${D1_LIMITS.entryBytes})`);
      return [{ data }];
    });
    return s.d1Out.get(id) ?? last;
  } catch (e) {
    if (e instanceof D1Fail) return { error: e.message, index: e.index };
    throw e;
  } finally {
    s.d1Want.delete(id);
    s.d1Out.delete(id);
  }
}

// bookmark is the local journal position, ordered as a string (upstream
// compares bookmarks with localeCompare).
function bookmark(s) {
  return String(s.seq).padStart(16, "0") + "." + s.incarnation;
}

async function sessionFresh(s, token) {
  if (!token || token === "first-unconstrained") return s.fresh(READ_FRESH_MS);
  if (token === "first-primary") return s.sync();
  const m = /^(\d{16})\.(.*)$/.exec(token);
  if (m && m[2] === s.incarnation && Number(m[1]) <= s.seq) return; // already at least there
  return s.sync();
}

// query runs one request: a statement or an array of them (a batch, or
// exec()'s lines when perLine).
async function query(s, { body, format, perLine, session }) {
  const many = Array.isArray(body);
  const qs = many ? body : [body];
  const fail = (error, index = 0) => {
    if (!perLine) return { success: false, error };
    const out = [];
    for (let i = 0; i < index; i++) out.push({ success: true, results: [], meta: { ...ZERO_META } });
    out.push({ success: false, error });
    return out;
  };
  if (qs.length === 0) return { body: many ? [] : fail("no statement"), bookmark: bookmark(s) };
  if (qs.length > D1_LIMITS.statements) return { body: fail(`too many statements: ${qs.length} (at most ${D1_LIMITS.statements})`), bookmark: bookmark(s) };
  let stmts;
  try {
    stmts = qs.map(norm);
  } catch (e) {
    return { body: fail(e.message, e.index), bookmark: bookmark(s) };
  }
  await sessionFresh(s, session);
  let r = null;
  if (!stmts.some((st) => !st.empty && likelyWrite(st.sql))) {
    r = trial(s, stmts, format);
    if (r.error === undefined && r.wrote) r = null; // it wrote after all: the write path
  }
  if (!r) r = await write(s, stmts, format);
  if (r.error !== undefined) return { body: fail(r.error, r.index), bookmark: bookmark(s) };
  return { body: many ? r.results : r.results[0], bookmark: bookmark(s) };
}

// quoteIdent quotes an SQL identifier.
const qi = (n) => '"' + String(n).replaceAll('"', '""') + '"';

// exportSQL dumps the database as SQL text (wrangler d1 export's shape):
// tables and their rows, then indexes, triggers and views.
function exportSQL(s, limit) {
  const sql = s.sql;
  const objs = sql
    .exec(
      "SELECT type, name, sql FROM sqlite_master WHERE sql IS NOT NULL AND name NOT LIKE 'sqlite\\_%' ESCAPE '\\' AND name NOT LIKE '\\_cf\\_%' ESCAPE '\\' " +
        "ORDER BY CASE type WHEN 'table' THEN 0 WHEN 'index' THEN 1 WHEN 'trigger' THEN 2 ELSE 3 END, rowid",
    )
    .toArray();
  const out = ["PRAGMA defer_foreign_keys=TRUE;"];
  let bytes = out[0].length;
  const push = (l) => {
    bytes += l.length + 1;
    if (bytes > limit) throw new D1Fail(`export too large: over ${limit} bytes`);
    out.push(l);
  };
  for (const o of objs) {
    push(o.sql + ";");
    if (o.type !== "table" || /^\s*CREATE\s+VIRTUAL\b/i.test(o.sql)) continue;
    const cols = sql.exec(`PRAGMA table_info(${qi(o.name)})`).toArray().map((c) => c.name);
    if (!cols.length) continue;
    const head = `INSERT INTO ${qi(o.name)} VALUES(`;
    const sel = `SELECT ${cols.map((c) => `quote(${qi(c)})`).join(" || ',' || ")} AS v FROM ${qi(o.name)}`;
    for (const r of sql.exec(sel)) push(head + r.v + ");");
  }
  return out.join("\n") + "\n";
}

registerStoreKind("d1", {
  init(s) {
    s.d1Want = new Map(); // request id -> results format, for writes in flight on this object
    s.d1Out = new Map(); // request id -> applied results
  },
  // apply throws when an entry can't be applied (not a D1 write, written
  // by a newer tailworks, or a statement that fails on replay, which only
  // non-deterministic SQL can do): the store stops its replay there and
  // is degraded (tw-store.js). Skipping it would leave a database that
  // differs from the one whose writes were acknowledged.
  apply(s, data, seq) {
    let e;
    try {
      e = decodeEntry(data);
    } catch (err) {
      if (err instanceof D1FormatError) throw err;
      throw new Error(`journal entry ${seq} is not a D1 write: ${err.message}`);
    }
    const format = s.d1Want.get(e.id);
    let r;
    try {
      r = execAll(s, e.stmts, format ?? "ROWS_AND_COLUMNS", format !== undefined);
    } catch (err) {
      const at = e.stmts.length > 1 ? ` (statement ${(err?.index ?? 0) + 1} of ${e.stmts.length})` : "";
      throw new Error(`D1 statement failed to apply${at}: ${String(err?.message ?? err)}`);
    }
    if (format !== undefined) s.d1Out.set(e.id, r);
  },
  reset(s) {
    // The store was deleted (or deleted and created again): drop every
    // tenant object. Views and triggers go with their tables.
    const objs = s.sql.exec("SELECT type, name FROM sqlite_master WHERE type IN ('table', 'view') AND name NOT LIKE 'sqlite\\_%' ESCAPE '\\' AND name NOT LIKE '\\_cf\\_%' ESCAPE '\\'").toArray();
    s.sql.exec("PRAGMA defer_foreign_keys = ON");
    for (const o of objs) s.sql.exec(`DROP ${o.type === "view" ? "VIEW" : "TABLE"} IF EXISTS ${qi(o.name)}`);
    // AUTOINCREMENT's counters: the table can't be dropped, only emptied.
    if (s.sql.exec("SELECT 1 FROM sqlite_master WHERE name = 'sqlite_sequence'").toArray().length) s.sql.exec("DELETE FROM sqlite_sequence");
  },
  ops: {
    query,
  },
  adminOps: {
    // query: `tw d1 execute` and migrations. Always catches up first.
    // {queries: [{sql, params}]} runs as one batch (atomic).
    async query(s, req) {
      const r = await query(s, { body: req?.queries ?? [], format: "ROWS_AND_COLUMNS", perLine: false, session: "first-primary" });
      return r.body;
    },
    // export: the database as SQL text. A degraded database exports what
    // it has applied, headed by a comment naming the entry that failed:
    // the way to recover it (export, delete, create, execute --file).
    async export(s, req) {
      let head = "";
      try {
        await s.sync();
      } catch (e) {
        if (e?.code !== "store_degraded") throw e;
        const d = s.degraded;
        head = `-- tailworks: degraded export: journal entry ${d?.seq} failed to apply and is not included, nor anything after it: ${String(d?.error ?? "").replaceAll("\n", " ")}\n`;
      }
      try {
        return { sql: head + exportSQL(s, Math.min(req?.limit ?? D1_LIMITS.resultBytes, D1_LIMITS.resultBytes)) };
      } catch (e) {
        if (e instanceof D1Fail) return { error: e.message };
        throw e;
      }
    },
  },
});

// D1Backend is the inner fetcher of the tenant's D1 binding (and, in a
// facet, the target of its env's D1 shim).
export class D1Backend extends WorkerEntrypoint {
  async fetch(req) {
    const u = new URL(req.url);
    const reply = (body, bm, status = 200) => {
      const h = { "content-type": "application/json" };
      if (bm) h[SESSION_HEADER] = bm;
      return new Response(JSON.stringify(body), { status, headers: h });
    };
    if (req.method !== "POST") return reply({ error: "D1 takes POST" }, null, 405);
    if (u.pathname === "/dump") return reply({ error: "dump() is not supported on tailworks; use `tw d1 export`" }, null, 501);
    if (u.pathname !== "/query" && u.pathname !== "/execute") return reply({ error: "not found" }, null, 404);
    let body;
    try {
      body = JSON.parse(utf8(toBytes(await req.arrayBuffer())));
    } catch {
      return reply({ success: false, error: "bad request body" });
    }
    const p = this.ctx.props;
    try {
      const r = await storeStub(this.ctx.exports, p.kind, p.name).op("query", [
        {
          body,
          format: u.searchParams.get("resultsFormat") ?? "ARRAY_OF_OBJECTS",
          perLine: u.pathname === "/execute" && Array.isArray(body),
          session: req.headers.get(SESSION_HEADER),
        },
      ]);
      return reply(r.body, r.bookmark);
    } catch (e) {
      // The journal or hostd (store_unavailable, lease_lost): a D1_ERROR.
      return reply({ success: false, error: String(e?.message ?? e) });
    }
  }
}
