Last update: 2026-11-15 09:00 PT. Platform-store substrate (ADR-platform-stores): Store DO + fenced, sealed journal in mgmt. Landed first so KV (#15), Queues, D1 and R2 can build on it.

# Platform stores (substrate for storage bindings)

Design: [ADR-platform-stores](../docs/design/adr/platform-stores.md).
This is the reusable "platform-owned storage binding" plumbing that issue
#15 asks to land first (AGENTS.md rule 6). It ships no user-visible binding
yet: KV is the first kind, on branch `workers-kv` (`status/workers-kv.md`).

**For the R2 / D1 / Queues workstreams:** a new storage binding is a
*kind*. You add:

1. `bundle.RegisterStoreKind` (wrangler key → `[]StoreBinding`);
2. `gen.RegisterStoreKind` (the workerd binding type: `kvNamespace`,
   `queue`, `r2Bucket` or `service`; your backend entrypoint and, for
   native bindings, a facet proxy entrypoint; and your platform JS module);
3. a JS module that calls `registerStoreKind(kind, {init, apply, reset,
   ops, alarm?})` from `twd-store.js` and exports your entrypoints.

Writes go through `store.commit(entries)` (journal first). Reads call
`await store.fresh(ms)` first. Pick `ckey`s so a later entry replaces an
earlier one. See `hostd/hostdtest/storeprobe.go` for a complete
30-line kind.

## Built

- **API** (`api/openapi.yaml`, tag `stores`; `make api`):
  - `GET …/workloads/{w}/stores` (admin, paged by kind/name, `?kind=`)
  - `GET|PUT|DELETE …/stores/{kind}/{name}` (admin; PUT is an idempotent
    create, 201/200; DELETE also deletes the data key; audited
    `store_create` / `store_delete`)
  - `GET|POST …/stores/{kind}/{name}/journal` (`admin|host`; hosts must
    give `?epoch=`, fenced like secret grants; POST is conditional on
    `after` + `incarnation`, `409 store_conflict`)
  - New `ErrorCode`s: `unknown_store`, `store_conflict`, `store_limit`. New
    `AuditKind`s: `store_create`, `store_delete`. New response
    `StoreConflict` (409, `Twd-Error` lease_lost | store_conflict).
- **mgmt** (`mgmt/stores.go`, `mgmt/sqlstore_stores.go`): `stores` and
  `store_entries` tables (SQLite and Postgres), keyed by incarnation.
  Entries sealed with AES-256-GCM under a per-store custody key (AAD =
  incarnation + seq), `ckey` stored as an HMAC, compaction in the append
  transaction. The host role's 1 MiB body limit is raised to 6 MiB for the
  append route only (`mgmt/v1.go` roleGate).
- **hostd**: `hostd/stores.go` (`/v1/store/read|append` on the local host
  API: adds tenant, workload and epoch, and allows only stores the
  deployment declares), `hostd/mgmtapi/stores.go` (client),
  `hostd/fakemgmt/stores.go` (in-memory journal with the same fencing and
  compaction).
- **bundle** (`hostd/bundle/stores.go`): kind registry and `App.Stores`.
  Binding names may not collide with DO bindings, vars or each other, and
  store names must match the API's pattern.
- **gen** (`hostd/gen/stores.go`, `hostd/gen/js/store.js`): the `Store` DO
  class in the platform worker (only when the app has stores), the kind
  modules, tenant bindings with props `{kind, name}`, and platform-side
  native bindings for facet proxies. `platform.js` gains a `//@@STORES@@`
  export marker and one line that adds the stores to a facet's env.
- **Docs**: the ADR draft, and `docs/errors.md` (`store_conflict`,
  `store_limit`, `store_unavailable`, `unknown_store`).

## Findings

- Upstream workerd (1.20261108.1) `kvNamespace`/`queue`/`r2Bucket` are
  frontends only: they speak HTTP to a named service and take `entrypoint`
  and `props`, which reach the target unchanged. So kinds can give tenants
  upstream's exact API and keep the store identity in props.
- A native KV binding can't go into a Dynamic Worker's env
  (`DataCloneError: A KV namespace binding cannot be serialized`), hence
  facet proxies.
- DO storage is host-local and nothing backs it up yet (RFC §failover's
  backup is unbuilt), so a store has to be durable off-host by itself.

## Tests (2026-11-15, macOS arm64)

- `go test ./mgmt/ -run 'TestStores|TestV1RoleMatrix|TestSpec'`: ok.
  - `TestStoresJournal`: conditional append, conflicts (behind, wrong
    incarnation), ckey compaction, paging, admin append without epoch, a
    host without an epoch is a 400, sealed at rest (no plaintext in mgmt's
    dir, logs or audit), `store_create` audit.
  - `TestStoresFence`: other host and wrong epoch get `lease_lost`; after
    `MoveLease` the old holder is refused and the new one reads everything.
  - `TestStoresLifecycle`: create (201/200), list and paging, delete
    (custody key gone), deleted store reads empty, old incarnation
    conflicts, a write from 0 starts a new incarnation.
  - `TestStoresLimits`: entry, append and ckey limits; a 4 MiB append is
    accepted.
- `go test ./hostd/ -run TestPlatformStore` (real workerd, testcontrol):
  ok. With the test-only probe kind: writes from the worker and from a
  facet, one journal append per write, catch-up after an admin append
  (the worker's next write conflicts and lands at seq 5), recovery of the
  whole store from the journal after the workload was fenced and its DO
  storage deleted, and an admin delete giving a new incarnation.
- `go test ./hostd/gen/ ./hostd/bundle/`: ok (`TestGenerateStores`,
  `TestParseStores`).
- `make lint`: ok.
- `make test`: every package ok except one transient: the `hostd`
  test binary's compile was killed (`signal: terminated`) under the
  parallel `-race` build. `go test -race -count=1 ./hostd/` was run again
  on its own: ok (93 s). `npm test`: 21/21.

## Gaps and next steps

- microVM tier: not run yet (expected to work unchanged; KV's e2e covers it).
- No tombstone compaction; no per-tenant quota across stores.
- No sharing of a store between workloads (needs a grant model; the KV ADR).
- `twd dev` stores are in-memory (the fake mgmt), so they last one
  `twd dev` run.
