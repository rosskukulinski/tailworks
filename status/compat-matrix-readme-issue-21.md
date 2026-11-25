Last update: 2026-11-15 PT. Compatibility matrix in the README, backed by tested docs/COMPAT.md rows (issue #21).

# Compatibility matrix (README + docs/COMPAT.md + lint)

## What's built

- **README "Compatibility at a glance"**: one table, one row per surface,
  grouped Runtime / Durable Objects / Bindings / Triggers / Platform / SDKs.
  Status ✅ / ⚠️ / ❌ / 🚫 and a short note; every row links its detail row
  (`docs/COMPAT.md#<id>`).
- **docs/COMPAT.md restructured**: a surface table per area on top, columns
  *workerd provides / twd adds / different from Cloudflare / missing /
  tests* (format after wdl-dev/wdl's `docs/compatibility.md`, no code taken).
  The older measured sections (Agents SDK, Vercel AI, containers, placement,
  public routes, Node tier) stay below under "Details". `## Secrets` became
  `## Secrets: details` so its slug doesn't collide with the `secrets` row
  anchor.
- **`make lint` → `compat-check`** (`internal/compatdoc`, run as
  `go run ./internal/compatdoc/compatcheck`). Fails when a README row has no
  COMPAT.md row (or the reverse), the statuses differ, a ✅/⚠️ row names no
  test, or a named test doesn't exist. A test ref is
  `` `<path>:<TestName>[/<subtest>]` ``: a Go package dir (`func TestName(t
  *testing.T)` in its `_test.go` files, any build tag; subtests as string
  literals or literal prefixes like `"ToolLoopAgent/"+k`) or a test file.
  Hermetic, needs nothing built. Adding a surface is one line in each file.
- **New measurements** (`hostd/gen/compat_test.go`, fixture
  `hostd/testdata/apps/compat`), on the generated platform + stock workerd,
  in the Worker, a facet object and a plain-DO object:
  - `TestRuntimeCompat/NodejsCompat`: `Buffer`, `node:crypto`,
    `AsyncLocalStorage`, `EventEmitter` ✅.
  - `TestRuntimeCompat/DOStorageKVAndSQL`: storage KV API (put/get/list/
    delete/transaction) and SQLite ✅ in both object modes (before this, only
    the KVM-only `e2e/vm` suite used the probe's `/kv` and `/sql` routes).
  - `TestRuntimeCompat/ConnectTCPRefused`: `connect()` fails everywhere
    ("cannot connect to the specified address"): the egress entrypoints
    (`WorkloadEgress`, `Egress`) are HTTP-only. ❌, pinned so a change shows.
  - `TestRuntimeCompatWasm`: a bundle with a `.wasm` module runs in all three
    places. But both bundlers (`internal/esbundle`, `create-twd/lib/build.mjs`)
    fail on `import x from "./x.wasm"` ("No loader is configured for .wasm
    files"), so the test assembles the bundle by hand, and the row is ⚠️.
  - `gen_test.go`: `start` split into `start` + `startFiles` (pre-built
    bundle), for the wasm test.

## Test results

- `go test -count=1 -run TestRuntimeCompat -v ./hostd/gen/`: PASS (macOS arm64).
- `go test -count=1 ./internal/compatdoc/...`: PASS (format, drift cases,
  and the repo's own docs).
- `make lint test` (macOS arm64, 2026-11-15): lint PASS (incl.
  `compat-check`); test-go PASS except `netd` `TestRejoinRejectedRemints`,
  which timed out (90 s) under load from the full run and passes alone
  (`go test -race -count=1 -run TestRejoinRejectedRemints ./netd/`: ok, 5 s);
  netd is untouched here. `make test-js`: 21/21 PASS.

## Findings

- `wrangler.jsonc` keys twd doesn't read (`kv_namespaces`, `r2_buckets`,
  `d1_databases`, `services`, `assets`, `queues`, `migrations`, `env`, …) are
  ignored without a warning; the binding is just `undefined` at run time. A
  deploy-time warning would help (follow-up; the KV / R2 / D1 / Queues /
  wrangler issues will each replace a ❌ row).
- Worker `fetch()` can't reach the public internet at all (`peer_not_visible`
  for names outside the netmap); `tailnet.egress.internet` only applies to
  containers today.

## Gaps / next

- `.wasm` loader in `internal/esbundle` (and `build.mjs`) to make wasm ✅.
- `connect()`: a `connect` handler on the egress entrypoints + netd TCP
  egress.
