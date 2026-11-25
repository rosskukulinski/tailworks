Last update: 2026-11-15 PT (second master merge: KV #37, R2 #35, agent DX #32; `make lint test` ok; `TestD1SurvivesRestartAndMove` ok). Architecture review: issue #38. D1 (issue #17, ADR-038): `d1_databases` binding on the platform-store substrate, wrangler migrations, `twd d1`, `twd dev`; survives a hostd restart and a lease move (multi-host e2e).

# D1 databases (issue #17)

Design: [ADR-038](../docs/design/adr/038-d1-databases.md) (draft, no number). User
guide: [docs/d1.md](../docs/d1.md). COMPAT rows: `d1`, `d1-sessions`,
`d1-migrations`, `d1-cli`. Builds on
[ADR-platform-stores](../docs/design/adr/platform-stores.md)
(`status/platform-stores.md`): D1 is the store kind `d1`.

## Built

- **Binding.** `hostd/bundle/d1.go` parses `d1_databases` (binding,
  `database_name` = the store name, `database_id` ignored,
  `migrations_dir`, `migrations_table`). `hostd/gen/d1.go` registers the
  kind: tenant code gets workerd's own D1 API, a `wrapped` binding of
  `cloudflare-internal:d1-api` whose `fetcher` is the platform's
  `D1Backend` with props `{kind, name}`.
- **Backend** (`hostd/gen/js/store-d1.js`): upstream's wire protocol
  (`/query`, `/execute`, results formats, the session header). Reads run
  locally in a transaction that proves they wrote nothing; writes are
  dry-run (rolled back), journaled as one entry (statements + params), then
  applied; the caller gets the applied results. Batch = one entry, atomic.
  Limits before SQLite runs and before results leave. Bookmarks are journal
  positions. `adminOps`: `query` (operator batches) and `export` (SQL).
- **Facets** (`js/packages/tailnet/src/d1.ts`, `make gen-js`): a facet's
  env gets a stub to `D1Backend`; the facet runtime wraps it in the same D1
  client API (`env.__TWD_WRAP` marker set by `platform.js`).
- **Operator path.** `hostd/storeadmin.go`: `Host.StoreAdmin` and the push
  route `POST /v1/store-admin` (one line in `hostd/http.go`) → the
  platform's `admin` route, op `store` (generic: any kind's `adminOps`).
  `mgmt/d1.go`: `POST /v1/tenants/{id}/workloads/{w}/d1/{name}/query` and
  `GET …/d1/{name}/export` (spec in `api/openapi.yaml`, `make api`), sent to
  the lease holder; `400 d1_error`; writes audited as `d1_write` (statement
  count, never SQL).
- **Migrations** (`internal/d1mig`): wrangler's layout and table, an SQL
  splitter (quotes, comments, trigger bodies), one atomic batch per
  migration with its record.
- **CLI** (`cmd/twd/d1.go`, one line in `main.go`): `twd d1 create | list
  | delete --yes | execute --command|--file | export | migrations
  create|list|apply`.
- **`twd dev`** (`cmd/twd/dev_d1.go`, small hooks in `dev.go`): applies
  pending migrations after each (re)deploy; `--json` reports them (`d1`).
- **Docs:** ADR draft, `docs/d1.md`, COMPAT rows + README, `docs/errors.md`
  (`d1_error`, `unknown_workload` for D1), `docs/twd-jsonc.md`.

### Shared plumbing changed (for the KV, R2 and Queues branches)

- `gen.StoreKindGen` gains `Binding: "wrapped"` (+ `WrappedModule`) and
  `FacetWrap` (a facet-runtime shim around a stub to the backend). Facets
  get `storeCfg.Wrap`; `platform.js` passes `env.__TWD_WRAP`.
- `store.js`: the substrate's meta (`seq`, `inc`, `synced`) moved from the
  `__twd_store_meta` SQL table to `ctx.storage.kv` (`__twd_store_<k>`), so a
  kind's SQL namespace is only its own. (No deployed stores existed; a
  store written before this change re-syncs from 0 and replays.) New
  `Store.adminOp` + kind `adminOps`.
- `hostd.Host.StoreAdmin` / `POST /v1/store-admin` and the platform's
  `admin` op `store` are kind-generic: `twd kv key get`-style operator
  reads can reuse them.

## Tests (2026-11-15, macOS arm64, workerd 1.20261108.1)

- `go test ./hostd/ -run 'TestD1$|TestPlatformStore' -count=1`: ok (5 s).
  `TestD1` on real workerd, worker and facet: `Binding`, `Migrations`
  (fixture's wrangler-layout files, incl. a trigger and a `';'` in a
  default; a failing migration applies nothing), `PreparedStatements`
  (first/first(col)/all/run meta/raw/raw columnNames, errors, `_cf_KV`,
  `BEGIN`, no platform tables visible, `dump()` throws), `Types` (integer,
  real, text, BLOB → byte array, null, 2^53+1 → 2^53, boolean → 1,
  bigint → `D1_TYPE_ERROR`, a bound number is REAL), `Batch` (atomic
  failure, facet batch with UPDATE + SELECT, `exec()` count and
  `Error in line 2`), `JournalFirst` (reads, sorted reads, CTE reads,
  no-op UPDATE and DDL append nothing; one append per write request; a
  writing CTE is caught), `Limits` (101 params, 100 KB statement, 2 MB
  value, 16 MiB result, 2 MiB write, none reach the journal), `Sessions`
  (increasing bookmarks, read from a bookmark in worker and facet),
  `AdminQueryAndExport`, `RecoveredFromJournal` (fenced + DO storage
  deleted → export identical except `CURRENT_TIMESTAMP` values), `Delete`
  (new empty database).
- `go test -tags e2eplacement -run TestD1SurvivesRestartAndMove
  ./e2e/placement/`: ok (6 s). Real twd-mgmt + two twd-hostd: writes via
  binding and via the mgmt API on h1, h1's hostd restarted (data there),
  `--replace` to h2 (data rebuilt from the journal, readable 0.21 s after
  the move; a new write on h2; h1 stops; journal head 4).
- `go test ./mgmt/ -run 'TestD1Routes|TestSpec|TestV1RoleMatrix'`: ok.
- `go test ./cmd/twd/ -run 'TestCLID1|TestDevD1|TestHelp'`: ok
  (`TestDevD1` runs `twd dev --once` with a custom migrations dir/table).
- `go test ./internal/d1mig/ ./hostd/bundle/ ./hostd/gen/`: ok.
- After merging `origin/master` (08d8962: wrangler-config loading,
  observability): `make lint test`: ok, every package and `npm test`
  (2026-11-15). `twd d1` and `twd dev` read the app's parsed wrangler
  config (so `wrangler.toml` and `--env` apps work); `bundle.IsStoreKey`
  already keeps the CLI from warning about `d1_databases`.

## Findings

- Stock workerd's `wrapped` D1 binding works without `experimental`; the
  HTTP protocol is the default (`d1_binding_jsrpc` gates `query()`).
- Durable Object SQLite binds every JS number as REAL (it can't bind a
  BigInt either), so `typeof(?)` of a bound `7` is `real`; columns with
  INTEGER affinity convert it. The same holds under `wrangler dev`
  (miniflare also runs D1 on DO SQLite).
- `rowsWritten` counts DDL (writes to `sqlite_schema`), which makes "did
  this request write?" exact without parsing SQL.
- `_cf_KV` shows in `sqlite_master` once the substrate writes its meta
  there; that matches D1, where `_cf_KV` is listed too. `sqlite_sequence`
  can't be dropped, only emptied (store reset does that).
- `CURRENT_TIMESTAMP` defaults (including wrangler's own `d1_migrations.
  applied_at`) get new values when a host rebuilds a database: statement
  replay. Documented; snapshot compaction would bound it.
- D1 wasn't in `e2e/sdk/cfagents_test.go`'s `UnsupportedBindings` list, so
  nothing to remove there.

## Gaps and next steps

- Snapshot compaction of the D1 journal (needs an mgmt "replace up to seq"
  entry); until then the journal keeps every write (1 GiB per database).
- Sharing a database across workloads (grant model, with platform-stores).
- microVM tier not run.
- `twd d1 export` capped at 16 MiB; `execute --file` is one batch (≤ 2 MiB
  encoded): no chunked import yet.
- `twd dev` databases last one run (the substrate's in-memory dev mgmt).
- No query-duration limit (DO SQLite can't interrupt a statement).
