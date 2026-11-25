Last update: 2026-11-15 PT. Merged master (#32 agent-first DX, #35 R2); design numbered ADR-037 (R2 took 036); full suite green. Merged to master.

# Workers KV (`kv_namespaces`)

Design: [ADR-037](../docs/design/adr/037-workers-kv.md) (draft; answers the
issue's five questions). User doc: [docs/kv.md](../docs/kv.md). Built on
[ADR-platform-stores](../docs/design/adr/platform-stores.md)
(`status/platform-stores.md`, landed first as its own PR, #27). KV is that
substrate's first real kind; nothing in the substrate had to change.

## Built

- **Kind** (new files only): `hostd/bundle/kv.go` (parses `kv_namespaces`;
  the store name is `id`, else the binding), `hostd/gen/kv.go` +
  `hostd/gen/js/kv.js`:
  - `KVBackend` speaks workerd's `kvNamespace` HTTP protocol (measured, see
    Findings) and runs each operation on the namespace's Store object;
  - `KVFacet` gives facets the same API over RPC;
  - the kind's `apply`/`reset`/`ops`/`alarm`: SQLite table
    `kv_entries(k, v, m, x, seq)`, expiry filtered on read and journaled as
    deletes by the alarm (128 per append).
- **Entry format** shared by JS and Go: `internal/kvwire`
  (`0x01 | u32 header len | {k,x?,m?,d?} | value`, ckey = key; Fold, Page,
  limits, checks), pinned by `TestEncodePinned`.
- **mgmt** (`mgmt/kv.go`): an admin-only KV view that folds the journal.
  - `GET …/stores/kv/{name}/keys` (prefix, cursor, limit);
  - `GET|PUT|DELETE …/stores/kv/{name}/value?key=`;
  - Cloudflare's rules; admin writes are audited `kv_put` / `kv_delete`
    with the namespace and actor, never the key (new `AuditKind`s, one per
    line).
- **CLI** (`cmd/twd/kv.go`): `twd kv namespace create|list|delete` and
  `twd kv key put|get|delete|list`, with `--binding` (from the wrangler
  config, `wrangler.toml` included) or `--namespace`; values from an
  argument, `--path` or stdin; `--ttl`, `--expiration`, `--metadata`;
  `twd audit` renders the two new kinds.
- **twd dev** (`cmd/twd/devstores.go`, `hostd/fakemgmt/persist.go`):
  namespaces persist between runs in `<app>/.twd/dev-stores.json` (0600; the
  directory git-ignores itself). The Store object rebuilds from the
  imported journal.
- **Runtime fix** (`js/packages/tailnet/src/runtime.ts`, regenerated
  `hostd/gen/js/twd-rt.js`): a default export that is a class instance
  (`export default new OAuthProvider(...)`) kept `fetch()` on its prototype
  and twd dropped it ("Handler does not export a fetch() function").
  `wrapHandler` now looks up the handler methods along the prototype chain.
- **Test helpers** (new files): `hostdtest` `Env.Restart()` and
  `Env.StartHost(id)` (a second twd-hostd on the same fakemgmt; `env.go`
  only stores its config), fakemgmt `MoveWorkload` (a forced takeover).
- **Docs**: COMPAT `#kv` row (❌ → ⚠️) and a "Workers KV: details"
  section, the Agents SDK table's `OAUTH_KV` row (✅), the pinned
  versions, two "found by this suite" bullets, `kv_namespaces` in the
  wrangler-config row; README row; `docs/wrangler.md` (KV is no longer
  warned about).
- `e2e/sdk/cfagents_test.go` `UnsupportedBindings`: `KV` removed from the
  list.
- New pinned devDependency: `@cloudflare/workers-oauth-provider` 1.2.4. The
  lockfile change is only its two entries; npm 11's re-annotation of
  `peer` flags was reverted to keep the diff mergeable.

## Findings

- **workerd's kvNamespace protocol** (pinned 1.20261108.1, measured with a
  standalone workerd):
  - `GET|PUT|DELETE /<key>?urlencoded=true` with `cache_ttl`,
    `expiration` and `expiration_ttl`; `cf-kv-metadata` on PUT;
    `CF-KV-Metadata` / `CF-Expiration` on GET responses; 404 → `null`;
  - `GET /?key_count_limit&prefix&cursor` for list (metadata as JSON text);
  - `POST /bulk/get {keys, type, withMetadata}`;
  - workerd checks empty and over-512-byte keys itself; TTLs reach the
    backend unchecked.
- **A KV `put()` hangs unless the backend reads the whole request body**
  before answering, even for an error; `body.cancel()` doesn't help. The
  backend reads (and drops, past the limit) the body first. For GET-shaped
  ops the tenant's error text is `statusText`, so errors set both.
- **workers-oauth-provider 1.2 needs `resourceMetadata.resource`** (an
  HTTPS URI) and compares it with `request.url`'s origin. twd hands Workers
  `http://` URLs (tailnet; public routes set `X-Forwarded-Proto`). The e2e
  app presents the canonical origin. Documented in COMPAT; a general
  "canonical URL" setting would be a separate change.
- **RFC 6749 §4.1.2 in practice**: replaying an authorization code makes
  the provider revoke the grant, i.e. delete its KV keys (my first test
  order replayed before calling the API, and the token was gone). The test
  now checks the revocation.
- `make test-sdk` in this worktree fails before any test (also on master's
  `TestCloudflareAgents`): under the suite's Seatbelt sandbox, workerd's
  Node launcher (`node_modules/.bin/workerd`) can't `lstat` the home
  directory from this worktree path. With
  `TWD_WORKERD=node_modules/@cloudflare/workerd-darwin-arm64/bin/workerd`
  everything passes. An environment issue, not KV; not changed here.
- Merging master (#28, wrangler config) was textually clean in its code,
  but semantically it conflicted: its tests asserted that `kv_namespaces`
  is warned about as unsupported. Its `bundle.IsStoreKey` already skips
  registered store kinds, so the warning disappears with KV registered. The
  tests and the toml fixture now use `r2_buckets` for the unsupported case,
  and assert that `kv_namespaces` is not warned about.

## Tests (2026-11-15, macOS arm64)

- `go test ./hostd/ -run 'TestWorkersKV$'` (real workerd, testcontrol): ok,
  5.6 s. Subtests: GetPutTypes (text/json/arrayBuffer/stream, empty value,
  odd keys), Metadata (incl. > 1024 refused), TTL (validation; expiry via an
  admin entry expiring in 3 s, gone from get/list/facet, then journaled as
  a delete by the alarm), ListPaging (25 keys at limit 10 → 10/10/5, cursor,
  limit 1001 and bad cursor refused), BulkGet (text/json/withMetadata, 101
  keys refused), DeleteAndLimits (a missing-key delete writes nothing,
  512-byte keys, a max-size value accepted, +1 byte → 413),
  FacetsAndSharing (every op from a facet, KV2 sharing `main`, OTHER
  separate), Journal (mgmt's journal folds to exactly what the worker
  lists).
- `go test ./hostd/ -run TestWorkersKVMove`: ok. A hostd restart, then a
  lease move to a second twd-hostd (own data dir and workerd, no DO
  storage), writes there, h1 fenced, moved back to h1 (stale local copy):
  catches up, all keys and metadata intact. Also: with mgmt's journal down
  (new fakemgmt `SetStoresDown`) and a hostd restart, reads fail static
  from the local copy, and a put or delete fails with 503 and doesn't land.
- `make e2e-vz`-style run of `e2e/vm:TestE2EVMKV` (Virtualization.framework,
  guest assets from `dist/vz-assets-arm64`, 2026-11-14): ok, 4.9 s. kv
  active in a microVM in 2.3 to 2.5 s, first put 83 to 222 ms (two runs),
  20 sequential gets 13 to 14 ms over the tailnet, data intact after a
  hostd restart (new VM). Run again after the master merge: ok.
  Command: `TWD_VZ_VMM=bin/twd-vz TWD_VM_KERNEL=…/vmlinux
  TWD_VM_ROOTFS=…/rootfs.ext4 go test -tags e2evm -run TestE2EVMKV
  ./e2e/vm`. Firecracker (Linux) not run.
- `go test -tags sdk -run 'TestOAuthProviderKV|TestCloudflareAgents'
  ./e2e/sdk` (with `TWD_WORKERD`, see Findings): both ok. OAuth:
  registration, PKCE S256 code flow, token, protected API sees props,
  bad token 401, refresh, `listClients`/`listUserGrants`, token keys carry
  expirations, data in mgmt's journal (head 6), code replay → 400 and grant
  revoked (keys deleted, token 401).
- `go test ./mgmt/ -run 'TestKVAdmin|TestV1RoleMatrix|TestStores'`: ok
  (admin view; hosts and non-admins refused on every new route, from the
  spec; audit records carry no key).
- `go test ./cmd/twd/ -run 'TestCLIKV|TestDevKVPersists|TestWrangler'`: ok.
- `go test ./internal/kvwire/ ./hostd/bundle/ ./hostd/gen/`: ok.
- JS: `node --test` in `js/packages/tailnet`: 22/22 (new: class-instance
  default export).
- After merging `origin/master` (#28 wrangler config, #29, #30
  observability): `make lint`: ok. `make test`: every Go package ok except
  `cmd/twd` (`TestCLIEndToEnd`, `TestCLILink`) and `hostd` (timeout in
  `TestContainerEgress`; `cmd/twd` also timed out in `TestDevOnce`). All of
  these are the Seatbelt + Node-launcher environment issue above (`twd
  doctor` in that run says so: "workerd … does not start under a twd
  Seatbelt profile"). Rerun with the native workerd:
  `TWD_WORKERD=…/workerd-darwin-arm64/bin/workerd go test -race -count=1
  ./cmd/... ./hostd/...`: all ok (cmd/twd 148 s, hostd 114 s). `npm test`:
  40/40. `e2e/sdk` (`TestOAuthProviderKV`, `TestCloudflareAgents`) and
  `e2e/vm:TestE2EVMKV`: ok after the merge.

## Merge with #32 (agent-first DX), 2026-11-15

- The design is numbered **ADR-037** (`docs/design/adr/037-workers-kv.md`,
  slug replaced everywhere, row in `docs/design/adr/README.md`).
- Semantic conflict: #32's `TestCLIJSONContract` requires every
  subcommand in `twd help` to have a `cliSurface` entry and a
  `schema/cli.schema.json` def. The `kv` usage now spells out each
  subcommand. The test now has 7 `kv` entries (`subDepth` 3), and the
  schema has 7 defs plus `kvKey`. `namespaces` and `keys` are always
  present (`[]` when empty). `TestCLIKV` checks every subcommand's real
  `--json` output against its def.
- `llms.txt` lists `docs/kv.md`. The `twd` skill's ask-first list names
  `twd kv namespace delete` and `twd kv key delete`.
- Tests after the merge (`TWD_WORKERD` = the native workerd, see
  Findings): `go test -race ./netd/... ./internal/... ./cmd/... ./mgmt/...
  ./hostd/... ./security/... ./guest/...`: all ok except
  `TestCLIJSONContract`, which the changes above fix. Then
  `./cmd/... ./schema/... ./docs/` were re-run under `-race`: ok.
  `npm test`: 40/40. `make lint`: ok.
- Not done: KV tools for twd's MCP server (ADR-035 picks its tools by
  hand).

## Merge with #35 (R2), 2026-11-15

- R2 took ADR-036 first, so Workers KV is **ADR-037**
  (`docs/design/adr/037-workers-kv.md`), and only KV's references were
  changed.
- Semantic conflicts:
  - each branch had used the other's binding as the wrangler config's
    "unsupported" example. With both supported, the tests, the TOML fixture
    and `docs/wrangler.md` use `d1_databases`, and assert that neither
    `kv_namespaces` nor `r2_buckets` is warned about;
  - `twd dev` store persistence exported every kind. R2's journal points
    at blobs in the dev process's temporary directory, so it would come
    back dangling. Persistence is now KV-only.
- Tests after the merge (`TWD_WORKERD` = the native workerd):
  `go test -race` over netd, internal, cmd, mgmt, hostd, security, guest,
  schema and docs: 33 packages ok. `npm test`: 40/40. `e2e/sdk`
  (`TestOAuthProviderKV`, `TestCloudflareAgents`): ok. `e2e/vm:TestE2EVMKV`
  (vz): ok. `make lint`: ok.

## Gaps and next steps

- Sharing a namespace between workloads (grant-gated, sketched in the ADR).
- Tombstone compaction (substrate): deletes and expiries stay in the
  journal until the key is written again, which matters for token-heavy
  apps.
- Values over 2 MiB − 4 KiB (needs an object store; the R2 work).
- `wrangler kv bulk put/delete`, import from a Cloudflare export.
- `twd kv` takes no `--env`: for an environment's workload, use
  `--workload <name>-<env> --namespace <id>`.
- The microVM tier ran on vz only; Firecracker not run.
- A canonical-URL setting (so code comparing `request.url` with an HTTPS
  identifier needs no shim) is not in scope.
