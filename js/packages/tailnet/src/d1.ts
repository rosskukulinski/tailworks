// D1 inside a facet (ADR-038). A facet-mode Durable Object runs in a Dynamic
// Worker, and workerd can't hand it the tenant worker's D1 binding (a
// wrapped binding is not serializable). Its env gets an RPC stub to the
// platform's D1Backend instead (the same backend the tenant worker's
// binding uses), and makeD1 puts the D1 API around it: the same wire
// protocol, results, errors and session bookmarks as workerd's
// cloudflare-internal:d1-api.

const SESSION_HEADER = "x-cf-d1-session-commit-token";
const FIRST_UNCONSTRAINED = "first-unconstrained";
const FIRST_PRIMARY = "first-primary";

type Fetcher = { fetch(input: string, init?: RequestInit): Promise<Response> };
type Meta = Record<string, unknown> & { duration?: number };
type Upstream = { success: boolean; error?: string; meta?: Meta; results?: unknown };
type Rows = { columns: string[]; rows: unknown[][] };

function d1Error(prefix: string, message: string): Error {
  return new Error(`${prefix}: ${message}`, { cause: new Error(message) });
}

function first<T>(r: T | T[]): T {
  return Array.isArray(r) ? (r[0] as T) : r;
}

function toObjects(r: Upstream): Upstream & { results: unknown[] } {
  const res = r.results as Rows | unknown[] | undefined;
  if (res === undefined) return { ...r, results: [] };
  if (Array.isArray(res)) return { ...r, results: res };
  return { ...r, results: res.rows.map((row) => Object.fromEntries(row.map((c, i) => [res.columns[i], c]))) };
}

class D1Session {
  #f: Fetcher;
  #token: string | null;
  constructor(f: Fetcher, token: string | null) {
    this.#f = f;
    this.#token = token;
  }
  get fetcher(): Fetcher {
    return this.#f;
  }
  prepare(sql: string): D1Statement {
    return new D1Statement(this, sql, []);
  }
  async batch(stmts: D1Statement[]): Promise<unknown[]> {
    const r = await this.sendOrThrow("/query", stmts.map((s) => ({ sql: s.statement, params: s.params })), "ROWS_AND_COLUMNS");
    return (Array.isArray(r) ? r : [r]).map(toObjects);
  }
  getBookmark(): string | null {
    return this.#token === FIRST_UNCONSTRAINED || this.#token === FIRST_PRIMARY ? null : this.#token;
  }
  protected update(b: string): void {
    b = b.trim();
    const cur = this.getBookmark();
    if (b && (cur === null || cur.localeCompare(b) < 0)) this.#token = b;
  }
  async send(path: string, body: unknown, format: string): Promise<Upstream | Upstream[]> {
    const headers: Record<string, string> = { "content-type": "application/json" };
    if (this.#token) headers[SESSION_HEADER] = this.#token;
    const resp = await this.#f.fetch("http://d1" + path + "?resultsFormat=" + format, { method: "POST", headers, body: JSON.stringify(body) });
    const bm = resp.headers.get(SESSION_HEADER);
    if (bm) this.update(bm);
    const text = await resp.text();
    let j: Upstream | Upstream[];
    try {
      j = JSON.parse(text);
    } catch {
      throw d1Error("D1_ERROR", `Failed to parse body as JSON, got: ${text}`);
    }
    const norm = (r: Upstream): Upstream => (r.error ? { success: false, error: r.error } : { success: true, meta: r.meta, ...("results" in r ? { results: r.results } : {}) });
    return Array.isArray(j) ? j.map(norm) : norm(j);
  }
  async sendOrThrow(path: string, body: unknown, format: string): Promise<Upstream | Upstream[]> {
    const r = await this.send(path, body, format);
    const f = first(r);
    if (!f.success) throw d1Error("D1_ERROR", f.error ?? "Something went wrong");
    return r;
  }
}

class D1AlwaysPrimary extends D1Session {
  constructor(f: Fetcher) {
    super(f, null);
  }
  override getBookmark(): null {
    return null;
  }
  protected override update(): void {}
}

class D1Database extends D1AlwaysPrimary {
  async exec(query: string): Promise<{ count: number; duration: number }> {
    const lines = query.trim().split("\n");
    const r = await this.send("/execute", lines.map((sql) => ({ sql })), "NONE");
    const rs = Array.isArray(r) ? r : [r];
    let duration = 0;
    for (const [i, x] of rs.entries()) {
      if (!x.success) throw d1Error("D1_EXEC_ERROR", `Error in line ${i + 1}: ${lines[i]}: ${x.error || "Something went wrong"}`);
      duration += Number(x.meta?.duration ?? 0);
    }
    return { count: rs.length, duration };
  }
  withSession(constraintOrBookmark?: string): D1Session {
    const t = constraintOrBookmark?.trim();
    return new D1Session(this.fetcher, t ? t : FIRST_UNCONSTRAINED);
  }
  async dump(): Promise<ArrayBuffer> {
    throw d1Error("D1_DUMP_ERROR", "dump() is not supported on tailworks; use `tw d1 export`");
  }
}

class D1Statement {
  readonly statement: string;
  readonly params: unknown[];
  #s: D1Session;
  constructor(s: D1Session, statement: string, params: unknown[]) {
    this.#s = s;
    this.statement = statement;
    this.params = params;
  }
  bind(...values: unknown[]): D1Statement {
    const out = values.map((v) => {
      const t = typeof v;
      if (t === "number" || t === "string") return v;
      if (t === "boolean") return v ? 1 : 0;
      if (t === "object") {
        if (v == null) return v;
        if (Array.isArray(v) && v.every((b) => typeof b === "number" && b >= 0 && b < 256)) return v;
        if (v instanceof ArrayBuffer) return Array.from(new Uint8Array(v));
        if (ArrayBuffer.isView(v)) return Array.from(v as unknown as ArrayLike<number>);
      }
      throw d1Error("D1_TYPE_ERROR", `Type '${t}' not supported for value '${String(v)}'`);
    });
    return new D1Statement(this.#s, this.statement, out);
  }
  async #one(path: string, format: string): Promise<Upstream> {
    return first(await this.#s.sendOrThrow(path, { sql: this.statement, params: this.params }, format));
  }
  async first(col?: string): Promise<unknown> {
    const rows = toObjects(await this.#one("/query", "ROWS_AND_COLUMNS")).results as Record<string, unknown>[];
    if (!rows.length) return null;
    if (col === undefined) return rows[0];
    if (rows[0]![col] === undefined) throw d1Error("D1_COLUMN_NOTFOUND", `Column not found (${col})`);
    return rows[0]![col];
  }
  async run(): Promise<Upstream> {
    return this.#one("/execute", "NONE");
  }
  async all(): Promise<Upstream> {
    return toObjects(await this.#one("/query", "ROWS_AND_COLUMNS"));
  }
  async raw(opts?: { columnNames?: boolean }): Promise<unknown[]> {
    const r = await this.#one("/query", "ROWS_AND_COLUMNS");
    const res = r.results as Rows | Record<string, unknown>[] | undefined;
    if (!res) return [];
    if (Array.isArray(res)) {
      const out: unknown[] = [];
      for (const row of res) {
        if (opts?.columnNames && out.length === 0) out.push(Object.keys(row));
        out.push(Object.values(row));
      }
      return out;
    }
    return [...(opts?.columnNames ? [res.columns] : []), ...res.rows];
  }
}

/** makeD1 is the D1 API over a stub to the platform's D1Backend. */
export function makeD1(fetcher: Fetcher): D1Database {
  return new D1Database(fetcher);
}

const WRAPS: Record<string, (stub: Fetcher) => unknown> = { d1: makeD1 };

/**
 * wrapStores replaces each env entry named in env.__TW_WRAP (set by the
 * platform for a facet's storage bindings) with its API, and drops the
 * marker. Outside a facet there is no marker and env is unchanged.
 */
export function wrapStores(env: Record<string, unknown>): void {
  const w = env.__TW_WRAP as Record<string, string> | undefined;
  if (!w || typeof w !== "object") return;
  delete env.__TW_WRAP;
  for (const [k, kind] of Object.entries(w)) {
    const f = WRAPS[kind];
    if (f && env[k]) env[k] = f(env[k] as Fetcher);
  }
}
