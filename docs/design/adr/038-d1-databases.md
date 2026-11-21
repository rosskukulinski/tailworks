# ADR-038: D1 databases are platform-store Durable Objects behind workerd's own D1 API, with statement journals in mgmt

**Date:** 2026-11-15
**Status:** Accepted (2026-11-16, built and shipped in v0.2.0: `d1_databases`,
`tw d1`, [docs/d1.md](../../d1.md); results in `status/d1-databases.md`). Builds on
[ADR-044](044-platform-stores.md).

**Context.**

- Issue #17 asks for the D1 binding (`d1_databases`, `env.DB.prepare().
  bind().first/all/run/raw`, `batch()`, `exec()`, `withSession()`) and
  wrangler's migration workflow. Tailworks had no D1. Durable Objects already
  have SQLite (`ctx.storage.sql`).
- **workerd has the frontend.** Stock workerd ships the D1 API as
  `cloudflare-internal:d1-api`, used through a `wrapped` binding whose
  inner `fetcher` binding is a service you supply (miniflare supplies one
  for `wrangler dev`). It sends `POST /query` and `POST /execute` with JSON
  statements (`{sql, params}` or an array of them, `?resultsFormat=`), and
  the session header `x-cf-d1-session-commit-token`. Measured on the pinned
  workerd (1.20261108.1): the wrapped binding works without the
  `experimental` flag, and `props` on the inner service binding reach the
  entrypoint unchanged.
- **The substrate exists.** ADR-044 gives every platform-owned
  storage binding a SQLite Durable Object in the platform worker, a
  journal in mgmt (conditional appends, fenced to the lease holder, sealed
  under a per-store custody key) and catch-up on start, on conflict and on
  stale reads. A kind registers how to apply an entry.
- **Measured properties of Durable Object SQLite** (pinned workerd) that
  the design relies on: a statement's cursor reports `rowsWritten`,
  including DDL (a `CREATE TABLE` writes 2 rows of `sqlite_schema`; `CREATE
  TABLE IF NOT EXISTS` of an existing table writes 0); nested
  `transactionSync` rolls back only the inner part; `BEGIN`/`SAVEPOINT` in
  SQL are refused (as on D1); `_cf_*` tables are not readable; JS numbers
  bind as REAL; integers come back as JS numbers (beyond 2^53 they lose
  precision, as on D1); BLOBs come back as `ArrayBuffer`; `PRAGMA
  foreign_keys` is on.

**Decisions.**

1. **Implementation: a D1 database is a platform store of kind `d1`.**
   Confirmed. Each `d1_databases` entry is the store `("d1",
   database_name)` of the workload: one platform-owned SQLite Durable
   Object (`Store`, `getByName("d1:<name>")`) whose SQL namespace holds
   only the tenant's tables. (The substrate keeps its own state in the
   object's synchronous KV API, workerd's hidden `_cf_KV` table, so
   `sqlite_master` lists the same tables D1 would.) That gives single-writer
   semantics per object, the lease fence, catch-up after a move, the
   microVM tier's encrypted volume, and RPO 0 for acknowledged writes,
   without new machinery.
   - Tenant code gets workerd's own D1 API (the wrapped binding); its
     fetcher is the platform's `D1Backend` entrypoint with props `{kind,
     name}` written by hostd. A facet-mode object can't be handed a wrapped
     binding (it doesn't serialize into a Dynamic Worker's env), so its env
     gets a stub to `D1Backend` and the facet runtime (`tw-rt.js`) puts a
     re-implementation of the same client API around it: the same wire
     protocol, results, errors and bookmarks.
   - **Reads** run on the object's local copy, in a transaction that checks
     they wrote nothing (`rowsWritten`, `total_changes()`); one that did (a
     CTE with DML) is rolled back and takes the write path.
   - **Writes are journal-first.** A request that may write is first run
     in a transaction that is always rolled back (the dry run: SQL errors,
     limits, and whether it writes at all). If it writes, its statements
     and parameters are one journal entry (`commit`), then applied in one
     transaction, and the caller gets the applied results (`changes`,
     `last_row_id`, `RETURNING` rows). A failing statement never reaches
     the journal. A batch is one entry and one transaction: atomic.
     `exec()` is a batch too (atomic; D1 runs it line by line). An `UPDATE`
     that matches nothing, or DDL that changes nothing, is not journaled.
   - **Statement replay, not row capture.** The journal holds statements;
     catch-up runs them again. Durable Object SQLite has no session
     extension and no update hook to capture row changes, and triggers on
     every tenant table would interfere with the tenant's schema. The cost:
     non-deterministic SQL (`random()`, `randomblob()`, `datetime('now')`,
     `DEFAULT CURRENT_TIMESTAMP`) is evaluated again when a host rebuilds a
     database (after a move or a lost disk). Locally the dry run and the
     apply run back to back under the store's write lock, so the caller's
     results are what was applied. An entry that fails on replay (only
     possible through non-deterministic SQL) is never skipped: replay
     stops before it and the database is degraded (`store_degraded`):
     it serves reads from what it applied, refuses writes, retries the
     entry on later syncs, and never reports itself synced past it. A
     skipped entry would silently diverge from the acknowledged writes
     (#106). Recovery: export what applied, recreate, re-import. Bind
     such values from JS where they matter.
   - There's no compaction: an entry has no `ckey`, because no later
     statement supersedes an earlier one. The journal keeps every write, up
     to the substrate's 1 GiB per store. Snapshot compaction is open
     (below).

2. **Sharing: one workload per database in v1.** A database belongs to the
   workload that binds it (and its replicas), like every platform store.
   Two workloads that bind the same `database_name` get two databases.
   Another workload reaches the data over the tailnet through the owning
   workload (its HTTP or RPC surface, gated by tailnet policy as any
   call is). The journal is a linearizable serialization point, so sharing
   can be added without changing the data path: a grant (a tailnet
   capability naming the owning workload and the database, checked by mgmt
   when it releases the journal to another workload's lease holder) is the
   platform-stores sharing design, and D1 will use it when it lands.

3. **Limits** are checked before SQLite sees a statement, and before a
   result leaves the store object. Statement text: 100 KB (D1's). Bound
   parameters: 100 per statement (D1's). A string or BLOB value: 2 MB
   (D1's). Statements per request: 1000. One write (statement or batch),
   encoded: 2 MiB (the journal's entry limit). Result: 16 MiB per request,
   counted while the rows are read (it stays under workerd's RPC message
   limit). Database: 1 GiB (`databaseSize`, after a write; the journal's
   own limit is reached first while there's no compaction). A query
   duration limit is not enforced (Durable Object SQLite can't interrupt a
   statement).

4. **Migrations: wrangler's format, applied explicitly.** `tw d1
   migrations create|list|apply <db>` reads the binding's
   `migrations_dir` (default `migrations`): `NNNN_<message>.sql` files in
   name order, recorded by file name in `migrations_table` (default
   `d1_migrations`, wrangler's schema: `id INTEGER PRIMARY KEY
   AUTOINCREMENT, name TEXT UNIQUE, applied_at TIMESTAMP DEFAULT
   CURRENT_TIMESTAMP NOT NULL`). Each migration is split into statements
   (quotes, comments, and `CREATE TRIGGER … BEGIN … END` understood) and
   applied together with its `INSERT` into the table as one batch, so it
   lands whole or not at all, and an existing wrangler project's
   migrations and table apply unchanged. Migrations are **not** applied at
   deploy, as with wrangler: a deploy of new code and a schema change are
   separate steps the operator orders. `tw dev`, whose databases live in
   the in-memory mgmt for one run, applies pending migrations after each
   (re)deploy, as `wrangler d1 migrations apply --local` would.

5. **Operator surface.** `tw d1 create|list|delete` are the generic store
   routes (`PUT|GET|DELETE …/stores/d1/{name}`; delete crypto-shreds).
   `tw d1 execute <db> --command|--file` and `tw d1 export` use two new
   routes, `POST /v1/tenants/{id}/workloads/{w}/d1/{name}/query` (a batch,
   `200` with D1's per-statement results, `400 d1_error` when a statement
   fails) and `GET …/d1/{name}/export` (SQL text in `wrangler d1 export`'s
   shape). Both are `x-tw-role: admin`. mgmt sends them to the host
   holding the workload's lease over the mgmt → hostd channel (`POST
   /v1/store-admin` on hostd's push API, like logs), and hostd hands them
   to its platform worker's admin route (`Store.adminOp`, a generic hook
   other kinds can use). So an operator write is journal-first like a
   binding write, and the database is reached only through the running
   deployment that binds it (`404 unknown_workload` otherwise). A write is
   audited (`d1_write`, with the statement count, never the SQL); a read
   is logged. Export is the backup: the journal in mgmt is the off-host
   durable copy and is backed up with mgmt's database (sealed).

6. **Sessions.** A bookmark is the store's journal position, zero-padded,
   plus its incarnation (`0000000000000042.si_…`), so upstream's
   `localeCompare` ordering holds. A read with a bookmark is served once
   the local copy has applied at least that position (else it catches up
   first); `first-primary` catches up first; a read without a session (or
   `first-unconstrained`) is served locally and catches up when the last
   sync is over 1 s old. With no read replicas, a session only matters
   across a workload's replicas, each of which has its own copy.

7. **Differences from Cloudflare** (listed in `docs/COMPAT.md`): no
   global read replicas, no Time Travel / point-in-time restore, no
   `dump()` (it throws; use `tw d1 export`), `database_id` ignored, a
   database scoped to a workload, a bound number stored as REAL without
   INTEGER affinity (as under `wrangler dev`), `exec()` atomic, a write
   at most 2 MiB encoded, every write waiting on mgmt, and non-deterministic
   SQL re-evaluated on a rebuild.

**Options considered and rejected.**

- *Run D1's SQL in mgmt* (mgmt's database or a SQLite per database in
  mgmt). Every read crosses the tailnet and an mgmt outage takes reads
  down; it also puts tenant SQL execution in the control plane.
- *Ship the SQLite file* (or its WAL) off-host. ADR-044
  rejected it: it needs workerd's private layout and a blob store, and
  loses writes acknowledged between two shipments.
- *Row-change journal via triggers.* Exact replay and compaction by row,
  but platform triggers on tenant tables change what the tenant sees
  (`sqlite_master`, trigger order, `changes()`), and DDL would have to be
  intercepted to keep them in place.
- *D1's JS RPC binding (`d1_binding_jsrpc`).* Upstream gates it behind a
  compatibility flag; the HTTP protocol is the default. The backend can add
  `query()` later.

**Consequences.**

- Existing D1 code and wrangler migrations run unchanged, in the Worker and
  in Durable Objects of both modes, with `tw dev` support.
- Each write costs a dry run, a journal append (one round trip to mgmt) and
  the apply. Reads cost local SQLite. Write-heavy hot paths belong in a
  Durable Object's own SQLite.
- mgmt's database holds the D1 data (sealed), and grows with every write
  until compaction exists.

**Open.**

- Snapshot compaction: an entry that replaces the journal up to a sequence
  number (a substrate change in mgmt: today compaction is only by `ckey`),
  which also bounds replay non-determinism to writes since the snapshot.
- Sharing a database across workloads by tailnet grant (with the
  platform-stores grant model).
- The microVM tier: the platform store substrate is expected to work
  unchanged there; D1 hasn't been run on it yet.
- Streaming exports over 16 MiB, and chunked imports over 2 MiB.
