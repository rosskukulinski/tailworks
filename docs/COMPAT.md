# Cloudflare Workers compatibility

What of the Cloudflare Workers platform works on tailworks, surface by surface.
It's **measured, not claimed**: every ✅ and ⚠️ row names the tests that
prove it, and `make lint` fails if one of those tests doesn't exist. The
README's [Compatibility at a glance](../README.md#compatibility-at-a-glance)
table (collapsed) is the summary of this page; the two must agree.

| Status | Meaning |
|---|---|
| ✅ | works as on Cloudflare, tested |
| ⚠️ | partial, or works differently (the row says how), tested |
| ❌ | not supported (yet) |
| 🚫 | not supported **by design** (the row says why) |

Each row says what **workerd** (the open-source runtime tailworks runs your code
on) provides, what **tailworks adds** on top, how it **differs** from Cloudflare,
what's **missing**, and the **tests**. Unless a row says otherwise, the
tests run under `make test`; `e2e/sdk` runs under `make test-sdk`,
`e2e/container` under `make e2e-container`, `e2e/node` under
`make test-node`, `e2e/placement` under `make e2e-placement`,
`e2e/observability` under `make e2e-observability`, `e2e/queues` under
`make e2e-queues`, `e2e/vitest` under `make test-vitest`.

**Adding or changing a row.** One surface is one table row (one line), so
parallel branches don't conflict. Its first cell starts with an anchor,
`<a id="<id>"></a>`; its second cell is the status; its last cell lists
tests as `` `<path>:<TestName>[/<subtest>]` `` (a Go package dir, or a test
file). Add the same `<id>` to the README table, linking
`docs/COMPAT.md#<id>` with the same status. Put new rows in their area's
table, in logical position, not at the end. A ✅ or ⚠️ row should cite
at least one test that CI runs on every PR (`make test`, `e2e/local`,
`e2e/vitest`); if every test it cites runs only nightly or by hand (the
other `e2e/` suites), its Tests cell must say "nightly". `make lint` runs
`internal/compatdoc` to check all of this.

**Cloudflare's own examples.** The tests above are written for tailworks.
`make e2e-compat` ([e2e/compat](../e2e/compat/README.md)) deploys 30 example
projects from Cloudflare's repositories unmodified (pinned commits from
cloudflare/templates, the create-cloudflare templates, cloudflare/agents and
cloudflare/ai) to a real host on a real tailnet, and calls each one as its
README does. The rows marked ❌ that those projects hit (service bindings,
Workers AI) are expected to fail there in the way the row says.

The same suite runs the **upstream projects' own examples** for the three
SDK rows, at pinned commits, under `tw dev` and deployed: every example in
`cloudflare/agents`, `vercel/mcp-handler` and the Vercel AI SDK is listed
with its result (pass, pass with a change, fail, or not run and why) under
[Agents SDK](#upstream-examples-agents-sdk),
[Vercel AI SDK](#upstream-examples-vercel-ai-sdk) and
[mcp-handler](#upstream-examples-mcp-handler), and every example in
`cloudflare/python-workers-examples` under
[Python Workers](#upstream-examples-python-workers). Those tables are generated
from the suite's results, and `make lint` fails if they are edited by hand.

## Runtime

| Surface | Status | workerd provides | tailworks adds | Different from Cloudflare | Missing | Tests |
|---|---|---|---|---|---|---|
| <a id="fetch"></a>`fetch` handler and outbound `fetch()` | ⚠️ | the `fetch` handler, `Request`/`Response`, streams, outbound `fetch()` through the worker's `globalOutbound` | requests arrive over WireGuard with the caller's identity (`getCaller`, [below](#identity)); outbound `fetch()` leaves from the workload's node, or from the object's own node (facet mode), so tailnet policy decides what each can reach | a tailnet name that isn't in the netmap is refused with `peer_not_visible` (tailnet names are never looked up on the host). Internet egress is **on by default**: a public host is reached from the host's own network, not from a tailnet node, so its source address is the host's ([recipes.md](recipes.md#call-an-internet-api)). Private, link-local (metadata), loopback, tailnet and host addresses are refused with `forbidden_destination`, checked on the address dialled after DNS. `tailnet.egress.internet: false` turns it off and a list limits it ([tailworks-jsonc.md](tailworks-jsonc.md#tailnetegressinternet)); a refused internet destination is `internet_egress_unavailable`. Inbound is tailnet-only unless you add [public routes](#public-routes). `request.cf` has no geolocation fields: `country`, `colo`, `city`, `region`, `latitude`, `longitude`, `timezone`, `asn` and the rest are absent (`undefined`, never `""` or `0`), for tailnet and public requests alike, so feature-detect them. tailworks sets only its own keys: `tailscale` (read it with `getCaller` / `getRequester`), `objectId`, `traceId` and `tw` ([details](wrangler.md#requestcf)). A tailnet request's `request.url` is `http://<name>.<tailnet>.ts.net/…`; with `tailnet.https: true` the workload is also served on `https://` (its ts.net certificate), and a request that arrives that way has an `https://` `request.url` ([tailworks-jsonc.md](tailworks-jsonc.md#tailnethttps)) | `request.cf` geolocation (no GeoIP lookup; a `100.x` tailnet caller has no IP location); a fixed internet source address (use a gateway on your tailnet) | `hostd:TestPublicIngress`, `hostd:TestTailnetHTTPS`, `netd:TestTailnetHTTPSListener`, `hostd:TestHostd/HelloVerifiedCaller`, `hostd:TestHostd/EgressAttributedPerObject`, `hostd/gen:TestGeneratedWrapperProbe/WorkloadEgressIsWorkload`, `netd:TestChain/PeerNotVisible`, `netd:TestChain/InternetEgressUnavailable`, `netd:TestInternetEgress`, `hostd:TestWorkerInternetEgress` |
| <a id="websockets"></a>WebSockets | ✅ | `WebSocketPair`, upgrade responses, the hibernation API | the upgrade is proxied through netd ingress and hostd to the object's own node | — | — | `e2e/sdk:TestCloudflareAgents/WebSocketsAndHibernation`, `netd:TestPublicUpgrade`, `e2e/vm:TestE2EVMWebSockets`, `e2e/dataplane:TestRouting` (N5: order, no loss, closed on fence, across moves and takeovers), `hostd:TestFenceClosesOpenWebSockets` |
| <a id="connect"></a>TCP sockets (`connect()`, `cloudflare:sockets`) | ⚠️ | `connect()`, and `connect()` handlers on the `globalOutbound` | raw TCP egress with `fetch()`'s identity and policy (ADR-052): from a facet-mode object as its own node, from the Worker and plain-DO objects as the workload node, to tailnet peers or (under `tailnet.egress.internet`) the internet, where TCP port 25 is refused unless an entry names it (`"smtp.example.com:25"`), as on Cloudflare. `ctx.tailnet.connect()` is the same with typed errors (and `tls: "netd"`); `tailnet.connects` takes a port (`"tag:db:5432"`). Flows are audited, traced and reset by a kill. [Recipe](recipes.md#connect-to-postgres-on-your-tailnet) | a refusal reaches stock `connect()` as "Network connection lost" (the typed code is in `tw audit`); at most 256 open sockets per object | `secureTransport: "on"` / `"starttls"` (workerd can't hand a TLS socket to a `connect()` handler: use TLS off on the tailnet, or `ctx.tailnet.connect(addr, { tls: "netd" })`); `ctx.tailnet.connect()` in plain-DO mode (`not_supported`) | `hostd/gen:TestConnectEgress`, `hostd:TestTCPEgress/Identity`, `hostd:TestTCPEgress/Refusals`, `hostd:TestTCPEgress/KillResetsSocket`, `hostd:TestTCPEgress/Audit`, `hostd:TestTCPEgressInternet`, `hostd:TestTCPEgressPostgres`, `e2e/vm:TestE2EVMTCPEgress` |
| <a id="wasm"></a>WebAssembly modules | ✅ | `.wasm` modules (`import mod from "./x.wasm"`), `WebAssembly.Instance` | the platform loads `.wasm` modules from the bundle, in the Worker and in both object modes; `tw deploy`'s bundlers ship them as wrangler does (`<sha1>-<file>.wasm`, also `.wasm?module`) | — | — | `hostd/gen:TestRuntimeCompatWasm`, `internal/esbundle:TestBuildAppWranglerSettings`, `e2e/wrangler:TestDevWrangler/BuiltinEsbuild` |
| <a id="nodejs-compat"></a>`nodejs_compat` | ✅ | the `node:*` modules behind `compatibility_flags: ["nodejs_compat"]` | the bundler maps bare Node builtins (`buffer`, `crypto`, …) to `node:*`, and turns a `require()` of one in a CommonJS dependency (`dotenv`'s `require("fs")`) into an import of it, as wrangler does; without the flag such a `require()` throws, when it runs, an error naming the package and the flag | — | only a sample is tested: `Buffer`, `node:crypto` (`createHash`, `randomUUID`), `AsyncLocalStorage`, `EventEmitter`; for real Node, see [Node apps](#node-runtime) | `hostd/gen:TestRuntimeCompat/NodejsCompat`, `internal/esbundle:TestRequireBuiltins`, `cmd/tw:TestDevRequireBuiltins` |
| <a id="python"></a>Python Workers | ⚠️ | Python Workers (Pyodide): `WorkerEntrypoint`, Python `DurableObject` (storage, RPC, WebSocket hibernation), `scheduled()` and `queue()` handlers, RPC into a Python `WorkerEntrypoint` over a service binding, static assets, the `workers` SDK, the `js` interop | `tw deploy` and `tw dev` ship `main`'s directory and the vendored `python_modules/` as pywrangler does, with no bundler; a generated JavaScript front checks the caller seal, so `request.js_object.cf.tailscale.caller` is the verified caller (`get_caller` in the `python` template); another Worker's RPC call over a service binding reaches the Python method through the front, which forwards it after the same seal check, and `get_caller(self.ctx)` names that caller; the pinned Pyodide bundles are fetched once per host, digest-checked, and read-only to the sandbox (on the microVM tier the guest image carries them), so workerd never downloads anything. [Details](python.md) | Python Durable Objects are internal objects (plain-DO mode, no tailnet node, no `ctx.tailnet`); packages are vendored before the deploy (`uv run pywrangler sync`), not installed by tailworks; from `compatibility_date` 2026-04-21 the `workers` SDK must be vendored too, or `disable_python_external_sdk` set; no memory snapshots | Workflows and containers for Python; `fetch()` to a named entrypoint of a Python Worker (RPC to it works); `--wrangler` for a Python Worker | `hostd/gen:TestPythonWorker`, `hostd/gen:TestPythonCallerUnsealed`, `hostd/gen:TestPythonCron`, `hostd/gen:TestPythonAssets`, `hostd/gen:TestGeneratePython`, `hostd:TestPythonQueues`, `hostd:TestPythonRPC`, `hostd:TestPythonWorkerSandboxed`, `e2e/vm:TestE2EVMPython`, `cmd/tw:TestDevPython`, `hostd/bundle:TestParsePython`, `internal/pyodide:TestBundlesMatchWorkerd`, [Cloudflare's Python examples](#upstream-examples-python-workers) |
| <a id="node-runtime"></a>Node.js apps (`runtime: "node"`) | ✅ | — (tw-only) | runs a plain Node.js process as a workload with its own tailnet node, egress through netd's proxy; runs the Vercel Workflow SDK. [Details](#node-process-tier-m9) | not a Cloudflare surface. One tailnet identity per process: no Durable Objects, no `tailnet.objects` | crons and public routes on the Node runtime | `e2e/node:TestNodeTier`, `e2e/node:TestNodeWorkflow`, `hostd:TestNodeSecrets` |

## Durable Objects

| Surface | Status | workerd provides | tailworks adds | Different from Cloudflare | Missing | Tests |
|---|---|---|---|---|---|---|
| <a id="do-identity"></a>One tailnet node per object | ✅ | — (tw-only) | every DO instance is its own tailnet node, with its own MagicDNS name, tags and grants; its egress is attributed to it; `tw kill` cuts it off | Cloudflare has no per-object network identity | `newUniqueId()` objects in facet mode (a name is the node's identity); in plain-DO mode they run but get no node ([egress-modes.md](egress-modes.md)) | `hostd:TestHostd/ObjectsGetOwnNodes`, `hostd:TestHostd/KillFailsFast`, `netd:TestChain/PerObjectEgressAttribution` |
| <a id="do-storage"></a>Storage: KV API and SQLite | ✅ | `ctx.storage` `get`/`put`/`list`/`delete`/`transaction`, `ctx.storage.sql` | storage on the host's local disk; it survives redeploys | an object lives on one host; there's no replication across hosts | point-in-time recovery (`getBookmarkForTime`, `onNextSessionRestoreBookmark`): not tested | `hostd/gen:TestRuntimeCompat/DOStorageKVAndSQL`, `hostd:TestHostd/BlueGreenRedeploy`, `e2e/sdk:TestCloudflareAgents/SetStateOnStateChangedSQL` |
| <a id="alarms"></a>Alarms | ✅ | `setAlarm`/`getAlarm`/`deleteAlarm`, `alarm()` | workerd facets can't set alarms, so tailworks delegates a facet's alarm to its Host DO (a shim); plain-DO objects use workerd's directly | — | — | `hostd/gen:TestGeneratedWrapperProbe/PlainDOAlarm`, `hostd:TestHostd/PlainDOAlarmWorkloadEgress`, `e2e/sdk:TestCloudflareAgents/Schedule`, `e2e/vm:TestE2EVMAlarms`, `e2e/dataplane:TestDataplane` (nightly, `alarms` and `cron` scenarios: plain-DO, facet and `scheduleEvery` alarms fire at least once and never on two hosts across kills, stalls, takeovers and moves; A1) |
| <a id="hibernation"></a>Hibernation | ✅ | idle eviction; WebSocket hibernation (`acceptWebSocket`, attachments) | the object's tailnet node stays up while the object is evicted | — | — | `hostd:TestHostd/HibernationKeepsNode`, `e2e/sdk:TestCloudflareAgents/WebSocketsAndHibernation`, `e2e/vm:TestE2EVMWebSockets`, `e2e/dataplane:TestRouting` (N5, `hibernate` fault) |
| <a id="locks"></a>Locks / leader election (pattern) | ⚠️ | single-threaded named objects, storage, alarms | a fenced owner per workload (ADR-011/028): a host cut off from mgmt stops serving before another can take over; the `lock` template: leases with renewal, holders recorded by tailnet identity, fencing tokens that carry the lease epoch so they keep increasing across a host move, `tw audit` of holders. [docs/locks.md](locks.md) | DO storage doesn't move: after a takeover a lock starts free on the new host (safe because leases are capped below the takeover window). Failover is manual (`--force-takeover`), about 45 s after the host's last heartbeat | automatic failover | `hostd:TestLockTemplate`, `e2e/locks:TestLocksFailover` |
| <a id="do-rpc"></a>RPC (`stub.method()`) | ⚠️ | JS RPC to DO stubs | RPC from the Worker and from a sibling object is forwarded into the facet | — | bindings to another Worker's class (`durable_objects` `script_name`) are refused at deploy; call that workload over the tailnet instead | `hostd/gen:TestGeneratedWrapperProbe/RPCForwardedToFacet` |
| <a id="facets"></a>Facets from tenant code (`ctx.facets`, `subAgent()`) | ❌ | Durable Object facets | tailworks itself runs each facet-mode object as a facet (that's how each object gets its own egress) | — | inside a facet-mode object, the Dynamic Worker's `ctx.exports` has no tenant classes, so the Agents SDK's `subAgent()` fails. Plain-DO mode: not tested. Use `ctx.tailnet.spawn` (own node, narrower caps) | `e2e/sdk:TestCloudflareAgents/UnsupportedBindings`, `hostd:TestHostd/SpawnChildNode` |

## Bindings

| Surface | Status | workerd provides | tailworks adds | Different from Cloudflare | Missing | Tests |
|---|---|---|---|---|---|---|
| <a id="kv"></a>Workers KV | ⚠️ | the `kvNamespace` binding (the API, its argument checks and types), backed by a service you supply | each namespace is a platform-owned SQLite Durable Object on the workload's host, with every write journaled (sealed, lease-fenced) in mgmt first, so it survives restarts and moves to another host; works in facets and on the microVM tier; `tw kv namespace\|key …` and REST, like `wrangler kv`; `tw dev` keeps it between runs. [Details](#workers-kv-details) | a namespace belongs to one workload; read-your-writes on a host, other writers seen within 60 s (or `cacheTtl`); writes need mgmt reachable; values ≤ 2 MiB − 4 KiB | sharing a namespace between workloads, values over 2 MiB, `wrangler kv bulk`, `cacheStatus` (always `null`) | `hostd:TestWorkersKV`, `hostd:TestWorkersKVMove`, `e2e/vm:TestE2EVMKV`, `e2e/sdk:TestOAuthProviderKV`, `mgmt:TestKVAdmin`, `cmd/tw:TestCLIKV`, `cmd/tw:TestDevKVPersists` |
| <a id="r2"></a>R2 | ⚠️ | R2 bindings backed by a service you supply | `r2_buckets` bindings: `get`, `put`, `head`, `delete` (one key or many), `list` (`prefix`, `delimiter`, `cursor`, `startAfter`, `include`), `httpMetadata` and `customMetadata`, MD5/SHA checksums, ranged reads (`range`, a `Range` header), `onlyIf` on get and put, in the Worker and in both object modes. The bucket's index is a platform store (journaled to mgmt, fenced to the lease holder, survives a move); the bytes are blobs in the host's R2 backend: files on the host (`-r2-backend fs`, the default) or any S3-compatible service (`s3`). Only hostd holds the backend's credentials; bodies stream through. `tw r2 bucket ls\|create\|rm`, `tw r2 object ls`. [Details](r2.md) | a bucket belongs to one workload; each write is a round trip to mgmt; with the `fs` backend the bytes stay on the host, so after a move to another host old objects fail with `r2_blob_missing` (use `s3` for more than one host) | public buckets, custom domains, presigned URLs and the S3 API to a bucket; lifecycle rules, event notifications, storage classes, `ssecKey`, jurisdictions; `tw r2 object get\|put`; sharing a bucket between workloads; [bucket mounts in containers](#containers) | `hostd:TestR2Binding/fs/worker/PutGetHeadMetadata`, `hostd:TestR2Binding/s3/facet/PutGetHeadMetadata`, `hostd:TestR2Binding/s3/worker/Ranges`, `hostd:TestR2Binding/fs/facet/OnlyIf`, `hostd:TestR2Binding/s3/worker/ListDelete`, `hostd:TestR2Streaming`, `hostd:TestR2IndexSurvivesMove`, `e2e/placement:TestR2MoveBetweenHosts`, `e2e/vm:TestE2EVMR2`, `hostd/r2:TestBackends`, `hostd/r2:TestSigV4AWSExample`, `hostd/bundle:TestParseR2Buckets`, `cmd/tw:TestCLIR2` |
| <a id="r2-multipart"></a>R2 multipart upload | ⚠️ | `createMultipartUpload`, `resumeMultipartUpload`, `uploadPart`, `complete`, `abort` | each part is a blob; `complete` commits the object, listing its parts, in one journal append; a ranged read reads only the parts it overlaps. Part rules as R2: all parts but the last the same size, at least 5 MiB | a part's etag is its MD5 | uploads that are never completed or aborted are kept (R2 aborts them after 7 days) | `hostd:TestR2Binding/fs/worker/Multipart`, `hostd:TestR2Binding/s3/facet/Multipart`, `hostd:TestR2IndexSurvivesMove` |
| <a id="d1"></a>D1 (`d1_databases`, `prepare`/`bind`/`first`/`all`/`run`/`raw`, `batch`, `exec`) | ⚠️ | the D1 API (`cloudflare-internal:d1-api`), which speaks HTTP to a backend you supply | each database is a platform-owned SQLite Durable Object (ADR-038); writes go to mgmt's fenced, sealed journal before they apply, so a database survives a host restart, a host loss and a lease move (`--replace`); reads are local. The same API in facet-mode objects. Limits checked before SQLite runs a statement and before a result leaves (100 KB statement, 100 parameters, 2 MB value, 16 MiB result). [Details](d1.md) | named by `database_name` within one workload (`database_id` is ignored); a bound JS number is a REAL in a column without INTEGER affinity (as under `wrangler dev`, also Durable Object SQLite); `exec()` is atomic; one write (statement or batch) is at most 2 MiB encoded; every write waits for mgmt; when a host rebuilds a database from the journal, `random()`, `CURRENT_TIMESTAMP` and other non-deterministic SQL run again; `dump()` throws (use `tw d1 export`) | sharing a database between workloads; read replicas; Time Travel; journal compaction (the journal keeps every write, up to 1 GiB per database); a query-duration limit; the `d1_binding_jsrpc` flag's `query()` | `hostd:TestD1`, `hostd/gen:TestGenerateD1`, `hostd/bundle:TestParseD1`, `e2e/placement:TestD1SurvivesRestartAndMove`, `e2e/vm:TestE2EVMD1` |
| <a id="d1-sessions"></a>D1 sessions (`withSession`, bookmarks) | ⚠️ | `withSession()`, `getBookmark()`, the `x-cf-d1-session-commit-token` header | a bookmark is the database's journal position: a read with a bookmark waits until the local copy has applied it; `first-primary` catches up with the journal first | there are no read replicas, so a session only matters across a workload's replicas. Without a session a read is served locally and catches up when its last sync is over 1 s old | — | `hostd:TestD1/Sessions` |
| <a id="d1-migrations"></a>D1 migrations (`migrations_dir`, `tw d1 migrations`) | ✅ | — | `tw d1 migrations create\|list\|apply`, wrangler's file layout and `d1_migrations` table (or `migrations_table`), each migration applied with its record as one atomic batch; `tw dev` applies pending migrations at start | `tw dev` applies them on its own (`wrangler dev` doesn't); `tw deploy` doesn't, as with wrangler | — | `internal/d1mig:TestMigrations`, `internal/d1mig:TestSplit`, `hostd:TestD1/Migrations`, `cmd/tw:TestCLID1`, `cmd/tw:TestDevD1` |
| <a id="d1-cli"></a>D1 operator commands (`tw d1`) | ⚠️ | — | `tw d1 create\|list\|delete\|execute --command\|--file\|export` (`execute` also reads SQL from stdin), like `wrangler d1`, through mgmt's API (`POST …/d1/{name}/query`, `GET …/d1/{name}/export`); a write is audited (`d1_write`, never the SQL) | `execute`, `export` and `migrations` reach the database through the running workload that binds it, not on their own; there is no `--local`/`--remote` (dev databases live in `tw dev`) | `export` of more than 16 MiB; import of a file over 2 MiB in one go | `cmd/tw:TestCLID1`, `mgmt:TestD1Routes`, `hostd:TestD1/AdminQueryAndExport` |
| <a id="queues"></a>Queues (producer) | ✅ | the `queue` binding (`send`, `sendBatch`, `metrics`, `contentType` `json`/`text`/`bytes`/`v8`, `delaySeconds`) backed by a service you supply | the broker: queues live in mgmt's database (sealed per queue), so producer and consumer can be different workloads on different hosts; `delivery_delay` per binding; facets get a proxy to the same binding; `tw queues`. [Details](queues.md) | a queue is created on first use (also `tw queues create`); `send()` errors read `Error: [<code>] … (<status>)` with tailworks' codes | per-queue settings (`message_retention_period`, a queue-level `delivery_delay`) | `hostd:TestQueues/ContentTypes`, `hostd:TestQueues/DelaySeconds`, `hostd:TestQueues/FromFacet`, `hostd:TestQueues/MetricsAndLimits`, `mgmt:TestQueuesSendPullAck`, `cmd/tw:TestCLIQueues`, `e2e/vm:TestE2EVMQueues` |
| <a id="workflows"></a>Workflows (`workflows`, `WorkflowEntrypoint`) | ✅ | the built-in engine (`/workerd/workflows.js`, `--experimental`): each instance a SQLite-backed Durable Object; `step.do` with retries, timeouts and delays, `step.sleep`/`sleepUntil`, `step.waitForEvent`, instance `create`/`createBatch`/`get`/`status`/`pause`/`resume`/`terminate`/`restart`/`sendEvent` | instances live in the workload's Durable Object storage on its lease holder (fenced with it; in the microVM's encrypted volume), so a move leaves them behind ([moves](approvals.md#moves)), with no tailnet node of their own; a Workflow's egress is the workload node's; the binding works in the Worker and in both object modes. [Approvals](#approvals) by tailnet identity | an event of type `approval` or `approval-*` can only come from tailworks' [approval surface](approvals.md), never from app code (`approval_requires_identity`), and no event the app sends may carry an `approver` (it is removed) | `script_name` (another Worker's Workflow) and `schedules` are refused at deploy; `subscribe()` and `deleteBatch` not tested | `hostd/gen:TestGeneratedWorkflows`, `hostd/gen:TestParseWorkflows`, `e2e/dataplane:TestDataplaneWorkflows`, `e2e/sdk:TestWorkflowApprovals/RetryFromFailedStep`, `e2e/sdk:TestWorkflowApprovals/SurvivesHostdRestart`, `e2e/vm:TestE2EVMWorkflows` |
| <a id="workers-ai"></a>Workers AI (`env.AI`) | ❌ | — | — | — | no binding. Point an AI SDK provider at an LLM gateway on your tailnet (the `ai-agent` template) | `e2e/sdk:TestCloudflareAgents/UnsupportedBindings` |
| <a id="vectorize"></a>Vectorize | ❌ | — | — | — | no binding | — |
| <a id="hyperdrive"></a>Hyperdrive | ❌ | — | — | — | no binding (`env.HYPERDRIVE`) and no pooling. A database driver over [`connect()`](#connect) reaches a database on your tailnet directly ([recipe](recipes.md#connect-to-postgres-on-your-tailnet)) | — |
| <a id="browser"></a>Browser Rendering (`env.BROWSER`) | ❌ | — | — | — | no binding | `e2e/sdk:TestCloudflareAgents/UnsupportedBindings` |
| <a id="email"></a>Email (`cloudflare:email`, `send_email`, `email()`) | ❌ | `EmailMessage`, the `email()` handler | the built-in bundler refuses an import of `cloudflare:email` from the app's own code (`not_supported`: "cloudflare:email is not supported on tailworks yet (imported by <file>)"; a workspace or linked package outside `node_modules` counts as the app's own code). A dependency's import under `node_modules` (the Agents SDK imports it at the top of its main module) gets a stub, and so does a pre-built `no_bundle`, Vite-built or `--wrangler` Worker, where tailworks can't tell whose import it is (the workerd config provides it); constructing an `EmailMessage` from the stub throws "not supported on tailworks" | — | sending and receiving. Funnel has no port 25: receive through a provider's webhook to a [public route](#public-routes) | `internal/esbundle:TestEmailImportRefused`, `internal/esbundle:TestEmailImportDependencyStubbed`, `cmd/tw:TestEmailImportRefused`, `e2e/sdk:TestCloudflareAgents/UnsupportedBindings`, `hostd/gen:TestEmailImportUnbundled` |
| <a id="service-bindings"></a>Service bindings (`services`) | ⚠️ | service bindings, JS RPC to a `WorkerEntrypoint` | `env.<binding>.fetch()` (streaming bodies, WebSocket upgrades) and RPC to the target's default export or a named `entrypoint`, between workloads of one tenant on the same host or another; the call leaves as the calling node (the workload, or the object in facet mode), so the target's `getCaller()` names it (in an RPC method: `getCaller(this.ctx)`). [Details](wrangler.md#service-bindings) | the call goes over the tenant's tailnet, so it needs a grant from the caller's tags to the target's (`tw policy`), like any tailnet call; RPC values must be structured-clone values (functions, `RpcTarget` stubs, streams, `Request`/`Response` are refused with `rpc_not_forwardable`); a request and its response stream one after the other, not full duplex | `environment`, `props`, bindings to another tenant's workload, RPC promise pipelining, `tw dev` bindings to another workload | `hostd:TestServiceBindings`, `internal/svcbind:TestFromWrangler`, `netd:TestChain/SameHostHop`, `cmd/tw:TestTypesEnv`, `e2e/dataplane:TestRouting` (N1–N3: moves, takeovers, revocation) |
| <a id="secrets"></a>Secrets | ⚠️ | text bindings | `tw secret put\|bulk\|ls\|rm` and `.dev.vars`, like `wrangler secret` (including `secret bulk`) and `wrangler dev`; `process.env` on [Node apps](#node-runtime). [Details](#secrets-details) | a secret overrides a var of the same name; a change takes effect on the next `tw deploy`; the microVM tier refuses workloads with secrets | secret versions, the Secrets Store binding | `hostd:TestWorkerSecrets`, `hostd:TestNodeSecrets`, `hostd/gen:TestGenerateSecrets`, `cmd/tw:TestCLISecretBulk` |
| <a id="vars"></a>Environment variables (`vars`) | ✅ | text and JSON bindings | the same `env` in the Worker and in both object modes; platform bindings are never visible to tenant code | — | — | `hostd/gen:TestGeneratedWrapperProbe/FacetEnvAndPrivacy` |
| <a id="assets"></a>Static assets (`assets`) | ⚠️ | `disk` services, service bindings | `tw deploy` / `tw dev` bundle `assets.directory` (`.assetsignore` honoured); hostd serves the files through a generated router Worker in front of the app: `Content-Type` by extension, `ETag` and `If-None-Match` (304), `html_handling` (all four modes), `not_found_handling` (`single-page-application`, `404-page`, `none`), `run_worker_first` (`true` or path patterns), `env.<binding>.fetch()`, `_redirects` (splats, placeholders, 200 rewrites) and `_headers`, assets-only projects (no `main`). `tw dev` reloads on an asset edit. [Details](wrangler.md#assets) | the files count against the 64 MiB bundle (about 48 MiB in one deploy request); anyone who can reach the workload can read a file that doesn't go through `run_worker_first`; `env.ASSETS` is in the Worker and plain-mode objects, not facet-mode ones | a separate upload path for large sites; domain-level `_redirects` / `_headers` rules | `hostd/gen:TestAssetsAutoTrailingSlash`, `hostd/gen:TestAssets404PageWorkerFirst`, `hostd/gen:TestAssetsSPAWorkerFirstAll`, `hostd/gen:TestAssetsSPANavigation`, `hostd/gen:TestGenerateAssetsFiles`, `hostd/gen:TestAssetsRedirects`, `hostd/gen:TestAssetsHeaders`, `hostd/gen:TestAssetsOnly`, `hostd/bundle:TestParseAssets`, `hostd/bundle:TestParseRedirects`, `hostd/bundle:TestParseHeaders`, `cmd/tw:TestBuildAssetsOnly`, `cmd/tw:TestAddAssets`, `cmd/tw:TestSnapshotAssetsDir`, `cmd/tw:TestTemplateTests/static-site` |
| <a id="pages"></a>Cloudflare Pages projects (`pages_build_output_dir`, `functions/`, `_worker.js`, `_routes.json`) | ⚠️ | — | `tw deploy` / `tw dev` compile `functions/` into the Worker (no wrangler needed) in front of the output directory as [assets](#assets): file routing (`index`, `[param]`, `[[catchall]]`, nested), `onRequest` and per-method handlers, `_middleware` at any level and handler arrays, the EventContext (`params`, `data`, `next()`, `waitUntil`, `passThroughOnException`, `functionPath`), `next()` and unmatched paths falling through to `env.ASSETS`, `_routes.json`, advanced-mode `_worker.js` (file or directory); the function routes become `run_worker_first` patterns; `tw dev` reloads on a `functions/` edit. [Details](wrangler.md#pages) | dashboard bindings and secrets must be declared in the config (`tw` warns for an undeclared `env.X` in `functions/`); `CF-Connecting-IP` is absent for tailnet callers | branch previews, the Pages build system, `CF_PAGES*` variables, Pages Plugins (`pluginArgs`), `wrangler pages` commands, `--wrangler` for Pages | `hostd/gen:TestPagesFunctionsRouting`, `hostd/gen:TestPagesFunctionsMiddleware`, `hostd/gen:TestPagesFunctionsAssets`, `hostd/gen:TestPagesRoutesJSON`, `hostd/gen:TestPagesAdvancedMode`, `internal/pages:TestDiscover`, `internal/pages:TestPatterns`, `internal/pages:TestParseRoutesJSON`, `internal/pages:TestEnvNames`, `cmd/tw:TestPagesFunctionsBuild`, `cmd/tw:TestPagesSPADefault`, `cmd/tw:TestPagesAdvancedBuild`, `cmd/tw:TestPagesDetection`, `cmd/tw:TestPagesEnvWarning` |

## Triggers

| Surface | Status | workerd provides | tailworks adds | Different from Cloudflare | Missing | Tests |
|---|---|---|---|---|---|---|
| <a id="cron"></a>Cron triggers | ✅ | the `scheduled()` handler | hostd runs `triggers.crons` on the host serving the active deployment (Cloudflare's syntax, UTC, 1 = Sunday). A run has no caller; its egress leaves from the workload's node. [Details](cron-triggers.md) | no backfill of missed minutes; an overlapping run is skipped, not queued; no retries; after a takeover, a stateless workload's in-flight run can overlap the new host's next run ([#566](https://github.com/rosskukulinski/tailworks/issues/566)) | crons on the Node runtime | `hostd:TestCronTriggers`, `hostd/gen:TestGeneratedCron`, `hostd/bundle:TestParseCrons`, `e2e/vm:TestE2EVMCron`, `e2e/dataplane:TestDataplane` (nightly, `cron` scenario: at most once per minute across hosts, no overlap, fenced, schedule changes; C1–C4) |
| <a id="queue-consumers"></a>Queue consumers (`queue()`) | ⚠️ | the `queue()` handler and `MessageBatch` (`ack`, `retry`, `ackAll`, `retryAll`) | hostd on the host holding the consumer's lease claims batches from mgmt (`max_batch_size`, `max_batch_timeout`), runs `queue()` and settles them; `max_retries`, `retry_delay`, `dead_letter_queue`; at-least-once across a workerd crash, a hostd restart and a lease move. [Details](queues.md#delivery) | several workloads may consume one queue (competing); `max_concurrency` is a fixed number of loops, not autoscaling | consumers on the Node runtime | `hostd:TestQueues/BatchBySize`, `hostd:TestQueues/BatchByTimeout`, `hostd:TestQueues/RetryWithDelay`, `hostd:TestQueues/MaxRetriesDeadLetter`, `hostd:TestQueues/ThrowRetriesBatch`, `hostd:TestQueues/ConsumerCrash`, `e2e/queues:TestQueuesMultiHost`, `cmd/tw:TestDevQueues` |
| <a id="queue-pull"></a>Queue pull consumers (HTTP pull) | ⚠️ | — | `POST …/queues/{q}/messages/pull` and `…/ack` on mgmt's API: lease IDs, visibility timeout, per-message retry delays, `maxRetries` and a dead-letter queue | tailworks' API, not Cloudflare's REST API; the caller needs tailworks' admin capability (tailnet identity, no token) | a per-queue grant for non-admin pull clients | `mgmt:TestQueuesDelayRetriesDLQ`, `mgmt:TestQueuesLongPoll`, `mgmt:TestQueuesFence` |

## Platform

| Surface | Status | workerd provides | tailworks adds | Different from Cloudflare | Missing | Tests |
|---|---|---|---|---|---|---|
| <a id="identity"></a>Caller identity and auth | 🚫 | — | `getCaller(req)`: the caller's tailnet node, user, tags and grant capabilities, from WireGuard; client copies of identity headers are stripped | by design there are no bearer tokens or API keys between workloads, and no Cloudflare Access JWTs: who's calling is the WireGuard peer, and what it may do is your tailnet policy (RFC) | on-behalf-of assertions (`actingFor`, RFC §4.6) | `hostd:TestHostd/HelloVerifiedCaller`, `hostd:TestHostd/ForgedHeadersStripped`, `netd:TestChain/ForgedIngressHeadersStripped` |
| <a id="approvals"></a>Workflow approvals by tailnet identity | ✅ | — (tw-only) | a Workflow that waits for an `approval` event (`waitForApproval`) is listed (`tw approvals`, the admin UI, `GET /_tw/approvals` on the workload's node) and decided only by a tailnet identity holding `github.com/rosskukulinski/tailworks/cap/approve` (workload, Workflow, step, decision and numeric-param limits), checked by tailworks before delivery; the Workflow sees `approver` (user, node, tags) from WireGuard; every request, decision, refusal and timeout is audited. [Details](approvals.md) | Cloudflare has no identity-bound approvals: there, any code holding the binding sends the event | email or chat notifications (the app sends its own); escalation beyond reject-on-timeout | `e2e/dataplane:TestDataplaneWorkflows`, `e2e/sdk:TestWorkflowApprovals`, `e2e/sdk:TestRefundApprovalTemplate`, `hostd:TestServeApprovalsRefusals`, `hostd:TestDecideApprovalOnce`, `hostd:TestAuthorizeApproval`, `hostd:TestApprovalRegistry`, `mgmt:TestApprovals`, `cmd/tw:TestApproveCLI` |
| <a id="placement"></a>Placement (`placement`) | ⚠️ | — | `"mode": "smart"` places a workload at deploy on the host with the best measured tailnet path to what it connects to. [Details](#workers-platform-config) | a different signal (path class and RTT, not request durations). By design, tailworks never moves a running workload on its own; `tw deploy --replace` re-places it. `placement.region` is ignored with a warning | per-object placement (`locationHint`) | `mgmt:TestScoreHosts`, `mgmt:TestAPIPlacement`, `mgmt:TestResolvePlacement`, `e2e/placement:TestSmartPlacementHermetic` |
| <a id="public-routes"></a>Public routes | ⚠️ | — | `public.routes` in `tailworks.jsonc` serves chosen paths over Tailscale Funnel; `getRequester(req)` returns `{kind: "public", ip}`; public requests can't create objects unless the class allows it. A public request's `request.url` is `https://`, as the client called it ([#360](https://github.com/rosskukulinski/tailworks/issues/360)). [Details](#public-routes-funnel-adr-029) | tailnet-only by default (Cloudflare Workers are public by default); `*.ts.net` names only | custom domains; `request.cf` geo fields | `hostd:TestPublicIngress`, `netd:TestPublicRoutes`, `netd:TestPublicHeadersAndBlob`, `hostd/gen:TestGeneratedWrapperPublic`, `e2e/dataplane:TestRouting` (N1, N4: moves, takeovers, route changes) |
| <a id="containers"></a>Containers (`ctx.container`, Sandbox SDK) | ⚠️ | — | each container is its own microVM attached to one Durable Object; egress through the object's identity, `enableInternet` limited to `egress.internet`. [Details](#containers-ctxcontainer-and-the-cloudflare-sandbox-sdk-adr-032) | needs a microVM-capable host (Linux with KVM, or macOS); amd64 not verified | `getTcpPort().connect()`, snapshots, `exposePort` / preview URLs, backups, bucket mounts | `e2e/container:TestE2EContainer`, `e2e/container:TestE2EContainerRegistry`, `hostd:TestContainerEgress` |
| <a id="logs"></a>Logs and tail | ⚠️ | `console.*` output; tail events (`TraceItem`) | `tw logs <workload>` shows one structured record per request: the verified tailnet caller, status, duration, outcome, `console.*` lines with their level, and exceptions (workerd's tail joined with tailworks' ingress), in the Worker and both object modes; `--json`, `--errors`, `--status`, `--object`, `--caller`, `--trace`; follows; works from a `tw link`ed machine (replicas prefixed by host); `--console` for the raw output. [Details](#logs-metrics-and-traces-adr-observability) | records live on the workload's host (rotating, rate-limited per workload) and are exported with OTLP, not queried in a dashboard; the filters differ from `wrangler tail`'s | Tail Workers for your code (`tail()`, `tail_consumers`), Workers Logs queries, `--search`/`--header`/sampling | `hostd:TestTelemetry/RecordsJoinIngressAndTail`, `hostd:TestTelemetry/ExceptionAndNoQueryString`, `hostd:TestTelemetry/LogsAPI`, `hostd:TestRecordRateLimit`, `hostd:TestLogsHandlerTailAndRefusals`, `mgmt:TestWorkloadLogs`, `cmd/tw:TestRemoteLogs`, `e2e/observability:TestObservabilityHermetic` |
| <a id="metrics"></a>Metrics (Workers metrics, Analytics) | ⚠️ | — | `GET /metrics` (Prometheus) on each host's and mgmt's own tailnet node, port 9464 (a grant in your tailnet policy decides who scrapes), and each workload's metrics over OTLP: requests, errors and latency (`http.server.request.duration`), egress per object, invocations by outcome, DO objects and wake-ups, host capacity and usage, mint stages. OpenTelemetry semantic-convention names. [Details](observability.md#metrics-get-metrics) | no dashboard or GraphQL Analytics API: bring Prometheus or a collector | CPU time (workerd reports 0); the Workers Analytics Engine binding | `hostd:TestTelemetry/PrometheusScrape`, `internal/obs:TestPrometheusExposition`, `internal/obs:TestSeriesLimitOverflow`, `e2e/observability:TestObservabilityHermetic` |
| <a id="traces"></a>Tracing (`traceparent`, Workers Traces) | ⚠️ | — | W3C `traceparent` across caller → Worker → Durable Object (stub `fetch()`) → egress → the next tailnet hop, a span per hop carrying the object and the verified caller, exported over OTLP. [Details](observability.md#traces) | trace context never feeds authorization or audit: the audit trace ID stays tailworks' own per hop; a public (Funnel) request starts a new trace | spans for DO RPC (`stub.method()`), alarms, cron runs and storage operations | `hostd:TestTelemetry/TraceparentAcrossDOAndEgress`, `hostd:TestTelemetry/OTLPExportFromTheWorkloadNode`, `hostd:TestIngressTraceContext`, `js/packages/tailnet/test/trace.test.js:a DO stub fetch gets a child span, reported when the handler ends`, `e2e/observability:TestObservabilityHermetic` |
| <a id="otlp-export"></a>Export to OpenTelemetry (Workers Observability destinations, Logpush) | ⚠️ | — | `tw telemetry set --otlp <url> [--header NAME]`: each workload's request records, metrics and spans over OTLP/HTTP (JSON); collector credentials kept in mgmt's custody like secrets. [Details](observability.md#export-to-a-collector-tw-telemetry-set) | by design the collector is a tailnet peer, dialed from each workload's own node, so your tailnet policy decides who may export where; no public destinations | per-workload config and sampling; Logpush to object storage | `hostd:TestTelemetry/OTLPExportFromTheWorkloadNode`, `mgmt:TestTelemetryRelease`, `mgmt:TestTelemetryRoles`, `cmd/tw:TestCLITelemetry`, `e2e/observability:TestObservabilityHermetic` |
| <a id="wrangler-config"></a>Wrangler config (`wrangler.toml`, `wrangler.json`, `wrangler.jsonc`) | ⚠️ | — | `tw deploy` and `tw dev` read whichever file wrangler would (json, then jsonc, then toml) and use `name`, `main`, `compatibility_date`, `compatibility_flags`, `vars`, `durable_objects`, `kv_namespaces`, `r2_buckets`, `migrations`, `triggers.crons`, `queues`, `assets`, `placement`, `containers`, `workflows` and the build settings; a project without a `tailworks.jsonc` gets defaults; a Cloudflare Vite plugin build is followed through `.wrangler/deploy/config.json` to the config it wrote, as wrangler does ([wrangler.md](wrangler.md), [Vite](wrangler.md#vite)) | only the app directory is searched (wrangler also searches parents) | the keys tailworks can't use (`routes`, `workers_dev`, binding sections it has no binding for, `site`, …) are ignored with one warning line each. [Details](#wrangler-config-details) | `cmd/tw:TestFindWranglerConfigPrecedence`, `cmd/tw:TestReadWranglerTOML`, `cmd/tw:TestWranglerWarnings`, `cmd/tw:TestLoadAppWranglerTOMLOnly`, `cmd/tw:TestCLIDeployWranglerTOML`, `cmd/tw:TestViteRedirectedConfig`, `cmd/tw:TestViteRedirectErrors`, `e2e/wrangler:TestDevViteRedirect`, `hostd/bundle:TestParseCrons`, `hostd/bundle:TestParseQueues`, `hostd/bundle:TestParseContainers`, `hostd/gen:TestParseWorkflows`, `hostd/bundle:TestParseKVNamespaces`, `mgmt:TestResolvePlacement` |
| <a id="wrangler-env"></a>Environments (`env.<name>`, `--env`) | ✅ | — | `tw deploy --env <name>` / `tw dev --env <name>` resolve `env.<name>` with wrangler's inheritance (`vars`, `define` and bindings are not inherited, warned per key as wrangler does) | each environment is its own workload, `<name>-<env>` unless the env sets `name`; `tailworks.jsonc` has no environments | — | `cmd/tw:TestApplyWranglerEnv`, `cmd/tw:TestCLIDeployWranglerTOML` |
| <a id="wrangler-bundling"></a>Bundling (`define`, `alias`, `rules`, custom builds) | ✅ | — | the built-in esbuild honours `define`, `alias`, `rules` (Text, Data, CompiledWasm, with wrangler's defaults), `minify`, `keep_names`, `jsx_*`, `tsconfig`; `--wrangler` bundles with `wrangler deploy --dry-run --outdir`, and `--from-wrangler-out <dir>` packs such a directory (needed for `no_bundle` except pre-built ES modules); a custom `build.command` runs before the built-in esbuild ([details](wrangler.md#custom-builds)); a pre-built `no_bundle` Worker (ESModule rules for only `.js`/`.mjs`, as the Vite plugin writes) is packed as it is ([details](wrangler.md#no-bundle)) | — | CommonJS modules as a `rules` module type (a CommonJS dependency of an ES-module Worker is bundled); `PythonModule` and `PythonRequirement` rules (a [Python Worker](#python) ships its `main` directory's files and `python_modules/` instead) | `internal/esbundle:TestBuildAppWranglerSettings`, `internal/esbundle:TestRuleGlobs`, `cmd/tw:TestBuildWranglerTOMLApp`, `e2e/wrangler:TestDevWrangler/FromWranglerOut`, `e2e/wrangler:TestDevWrangler/WranglerCLI`, `cmd/tw:TestPrebuiltNoBundle`, `cmd/tw:TestPrebuiltNoBundleDirect`, `e2e/wrangler:TestDevViteRedirect`, `cmd/tw:TestBuildRunsBuildCommand`, `cmd/tw:TestDevBuildNoLoop` |
| <a id="wrangler-api"></a>`wrangler deploy`, `wrangler secret`, `wrangler tail` against tailworks | 🚫 | — | `tw deploy`, `tw secret`, `tw logs -f` take the same project | tailworks serves no Cloudflare-compatible API: it would have to track about 50 wrangler endpoints across frequent wrangler releases and bring bearer tokens back, and preflight, placement and image pinning don't fit wrangler's flow ([why](wrangler.md#wrangler-commands-that-call-cloudflare)) | — | — |

## SDKs

| Surface | Status | workerd provides | tailworks adds | Different from Cloudflare | Missing | Tests |
|---|---|---|---|---|---|---|
| <a id="agents-sdk"></a>Cloudflare Agents SDK (`agents`) | ⚠️ | — | runs unmodified: routing, state, SQL, schedules, `@callable` RPC, WebSockets and hibernation, MCP client and server (`createMcpHandler`, `McpAgent`), in both object modes. `getCaller` in `onRequest` and `onConnect`. `AgentWorkflow` and `waitForApproval`, with the approver's verified identity. `AIChatAgent` turns with server and approval tools, and `PiHarness` (Pi Durable) with a tool. [Details](#cloudflare-agents-sdk-agents-0340). Cloudflare's own examples, one row each: [upstream examples](#upstream-examples-agents-sdk) | an agent can't call an MCP server on the same host over the tailnet (`forbidden_destination`); `approveWorkflow()`/`rejectWorkflow()` from app code are refused by design ([approvals](#approvals)); agents reached through the imported env (`import { env } from "cloudflare:workers"`) can't be created (`create_not_admitted`: pass the handler's `env`) | `subAgent()`, `env.AI`, email, browser, codemode. Most of Cloudflare's examples need Workers AI or another of these and were not run, and some of those that ran fail ([each one, and why](#upstream-examples-agents-sdk)) | `e2e/sdk:TestCloudflareAgents/RouteAgentRequest`, `e2e/sdk:TestCloudflareAgents/Schedule`, `e2e/sdk:TestCloudflareAgents/AgentAsMCPClient`, `e2e/sdk:TestAgentTemplate`, `e2e/sdk:TestWorkflowApprovals`, `e2e/compat:TestCompatDev` (nightly: `make test-sdk`, `make e2e-compat-dev`), `cmd/tw:TestTemplateTests/chat-agent`, `cmd/tw:TestTemplateTests/pi-agent` |
| <a id="mcp"></a>MCP servers (`createMcpHandler`, `mcp-handler`) | ✅ | — | `@tailworks/mcp` `withTailnet`: a per-request caller, and tools allowed or denied by tailnet grants (`cap_missing`). mcp-handler's own examples, one row each: [upstream examples](#upstream-examples-mcp-handler) | mcp-handler's examples are route modules, so each needs a Worker entry that mounts it | stdio transport. An OAuth-protected server (workers-oauth-provider wants an `https://` resource URL) needs `tailnet.https: true` ([#360](https://github.com/rosskukulinski/tailworks/issues/360)) | `e2e/sdk:TestCloudflareAgents/CreateMcpHandlerWithTailnet`, `e2e/sdk:TestVercelAI/McpHandlerWithTailnet`, `e2e/compat:TestCompatDev` (nightly: `make test-sdk`, `make e2e-compat-dev`), `cmd/tw:TestTemplateTests/chat-agent`, `cmd/tw:TestTemplateTests/pi-agent` |
| <a id="vercel-ai"></a>Vercel AI SDK (`ai`) | ⚠️ | — | `ToolLoopAgent` and UI message streams run in the Worker and in objects (one agent, one node). [Details](#vercel-ai-sdk-ai-70177-and-mcp-handler-240). The AI SDK's own examples, one row each: [upstream examples](#upstream-examples-vercel-ai-sdk) | `@ai-sdk/mcp` with the default `fetch` throws in workerd; use `@tailworks/mcp` `mcpFetch`. None of the AI SDK's examples targets Workers: the three that ran are Node servers given a Worker entry, with a scripted model | `WorkflowAgent` in the Worker (it runs on [Node apps](#node-runtime)), on-behalf-of assertions; the AI SDK's Next.js, Nuxt and SvelteKit examples (not run) | `e2e/sdk:TestVercelAI/ToolLoopAgent`, `e2e/sdk:TestVercelAI/EgressRefusalIsTyped`, `e2e/node:TestNodeWorkflow`, `e2e/compat:TestCompatDev` (nightly: `make test-sdk`, `make test-node`, `make e2e-compat-dev`) |

## Tooling

| Surface | Status | workerd provides | tailworks adds | Different from Cloudflare | Missing | Tests |
|---|---|---|---|---|---|---|
| <a id="cli-json"></a>CLI `--json` output | ✅ | — (tw-only) | every command's result is one JSON document matching [`schema/cli.schema.json`](../schema/cli.schema.json); every error is `{code, message, hint, doc_url}` with a code from [errors.md](errors.md); `tw deploy` streams progress events (stderr), `tw dev` and `tw logs` stream NDJSON (stdout). [Details](cli-json.md) | not a Cloudflare surface (wrangler's `--json` covers some commands) | `status`'s `workloads[]`, `image push` and `host vmcheck --container` use Go field names | `cmd/tw:TestCLIJSONContract`, `cmd/tw:TestAgentEval`, `cmd/tw:TestDevJSONEvents`, `cmd/tw:TestRemoteLogs` |
| <a id="agent-skills"></a>Coding-agent skills (`tw agent`) | ✅ | — (tw-only) | ten skills built into the CLI (scaffold, dev, deploy, debug, policy, secrets, cron, public routes, teardown); `tw agent setup` installs them for Claude Code, Codex, Cursor, Gemini CLI and OpenCode, per user or per project, after showing what it writes; `tw agent remove` undoes it. [Details](agents.md) | not a Cloudflare surface | per-user Cursor rules (Cursor keeps them in its settings) | `cmd/tw:TestSkillCommandsExist`, `cmd/tw:TestSkillsWellFormed`, `cmd/tw:TestAgentEval`, `cmd/tw:TestAgentSetupSnapshot`, `agent:TestPlanBlock`, `agent:TestPlanStaleAndForeign` |
| <a id="vitest-plugin"></a>Testing: `@cloudflare/vitest-plugin` | ⚠️ | — (Cloudflare's test runner; it brings its own workerd via miniflare) | `@tailworks/tailnet/testing`: `withCaller` / `withPublic` (a caller in `request.cf`, which survives service bindings and Durable Object stubs) and a fake `ctx.tailnet` (spawn, kill, audit, self, fetch); black-box tests through `tw dev --caller` and `@tailworks/tailnet/testing/dev`; every template ships an `npm test`. [Details](testing.md) | plain Worker, Durable Object (storage, alarms), Agents SDK, Workflows and MCP-handler logic run as on Cloudflare; `getCaller` and `ctx.tailnet` need the helper (nothing delivers a caller in vitest-plugin) | approvals and per-object egress can only be tested against `tw dev`; `tw dev` can't choose a caller's login and has no policy evaluator yet; no `withCaller` for the Node runtime | `e2e/vitest/test/lock.test.ts:withCaller reaches getCaller through the Worker and the Durable Object stub`, `e2e/vitest/test/agent.test.ts:Agents SDK state and SQL: one agent per caller`, `e2e/vitest/test/mcp.test.ts:a granted tool runs; a tool outside the grant is cap_missing; a limit is enforced`, `e2e/vitest/test/refund-approval.test.ts:a refund request starts a Workflow that waits for approval`, `js/packages/tailnet/test/testing.test.js:withCaller refuses to run outside test mode`, `cmd/tw:TestDevCallers`, `cmd/tw:TestTemplateTests` |
| <a id="tw-mcp"></a>tailworks' MCP server (`/mcp`, `tw mcp`) | ✅ | — (tw-only) | mgmt serves `whoami`, `status`, `ls`, `logs`, `audit`, `deploy`, `kill` and `undeploy` as MCP tools next to `/v1`, authorized by the caller's tailnet identity; `deploy`, `kill` and `undeploy` only with a `github.com/rosskukulinski/tailworks/cap/mcp` grant naming them; `tw mcp` is a stdio bridge for any agent. [Details](agents.md#tws-mcp-server), [ADR-035](design/adr/035-mgmt-mcp.md) | not a Cloudflare surface | tools for secrets, tenants, hosts, expose and telemetry; `deploy` takes a bundle (or redeploys the current one), it doesn't build | `mgmt:TestMCPServer`, `cmd/tw:TestAgentEval` |
| <a id="docs-for-agents"></a>Docs for agents (`llms.txt`, `tw docs`) | ✅ | — (tw-only) | [`llms.txt`](../llms.txt) indexes the docs; `tw docs search` and `tw docs show <doc>#<anchor>` read the docs built into the CLI, offline | not a Cloudflare surface | — | `docs:TestLLMsTxt`, `docs:TestShowAndSearch` |

---

# Details

The sections below are the detailed measurements behind the rows above.

## SDK conformance (process tier)

What runs in-isolate on tailworks, as **measured** by the hermetic conformance suite
`make test-sdk` (`e2e/sdk`): real SDK code on tw-hostd + the generated
platform + stock workerd, driven over testcontrol + local DERP by a tsnet
client, on macOS (arm64) and Linux (Lima `tw-dev`, arm64). No real tailnet,
no LLM keys.

Pinned versions (root `package.json`, exact):

| Package | Version | Note |
|---|---|---|
| workerd | 1.20261121.1 | platform runtime; the built-in Workflows engine (`/workerd/workflows.js`, workerd#8331) is in 1.20261114.1 and later |
| `agents` (Cloudflare Agents SDK) | 0.34.0 | npm `latest` on 2026-11-23; runs on the pinned workerd unmodified. `agents/harness/pi` needs 0.34.0 or later |
| `@cloudflare/ai-chat` | 0.12.3 | `AIChatAgent` (`chat-agent` template) |
| `@earendil-works/pi-durable` / `pi-ai` / `pi-schema` / `chord` | 1.6.0 | Pi, for `PiHarness` (`pi-agent` template); agents 0.34 wants `~1.6.0` |
| `@modelcontextprotocol/server` / `client` | 2.4.0 | MCP SDK v2 (agents' peers; `createMcpHandler`, `addMcpServer`) |
| `@modelcontextprotocol/sdk` | 1.32.2 | v1, only for agents' deprecated `McpAgent` (not used) |
| `ai` (Vercel AI SDK) | 7.0.177 | `ToolLoopAgent`, `ai/test` `MockLanguageModelV4` |
| `@ai-sdk/provider` / `provider-utils` | 4.0.39 / 5.0.82 | |
| `@ai-sdk/mcp` | 2.0.101 | HTTP transport only |
| `mcp-handler` (Vercel) | 2.4.0 | MCP SDK v2, 2026-07-28 spec |
| `zod` | 4.6.7 | |
| `@cloudflare/workers-oauth-provider` | 1.2.4 | `OAUTH_KV` on tailworks' KV binding (`TestOAuthProviderKV`) |

Egress modes (ADR-019 #5, `docs/egress-modes.md`): **facet** (default; the
object's `fetch()` leaves from its own node) and **plain-DO**
(`"egress": "workload"`; egress leaves as the workload node). Every Agent
check below runs in both.

## Cloudflare Agents SDK (`agents` 0.34.0)

| Feature | Facet | Plain-DO | Test (`e2e/sdk`) | Notes |
|---|---|---|---|---|
| `routeAgentRequest` (`/agents/<kebab-class>/<name>`) | ✅ | ✅ | `RouteAgentRequest` | unknown namespace → 400 (SDK behaviour) |
| `getAgentByName` | ✅ | ✅ | `RouteAgentRequest` | |
| `getCaller` in `onRequest` | ✅ | ✅ | `RouteAgentRequest`, `SetState…` | via the workload node and via the object's own node |
| `getCaller` in `onConnect` (`ctx.request`) | ✅ | ✅ | `WebSocketsAndHibernation` | the upgrade request keeps `cf` |
| `setState` / `onStateChanged` | ✅ | ✅ | `SetStateOnStateChangedSQL` | `initialState`'s first persist also fires `onStateChanged` (SDK behaviour) |
| `this.sql` | ✅ | ✅ | `SetStateOnStateChangedSQL` | |
| `schedule(delay)` / `scheduleEvery` / `listSchedules` / `cancelSchedule` | ✅ (shim) | ✅ | `Schedule`, `TestAgentTemplate` | workerd facets cannot set alarms; tailworks delegates the facet's `ctx.storage.{set,get,delete}Alarm` to its Host DO, whose `alarm()` runs the facet's. Once + 2× every fired in ~1.9 s in both modes |
| WebSockets (partyserver), state-sync broadcast (`cf_agent_state`) | ✅ | ✅ | `WebSocketsAndHibernation` | through netd ingress + hostd (`httputil.ReverseProxy` upgrade) |
| WebSocket **hibernation** | ✅ | ✅ | `WebSocketsAndHibernation` | 14 s idle: the DO was evicted (new boot ID), sockets stayed open, connection state (attachments, SQL) intact |
| Plain `fetch()` egress attribution | own node | workload node | `EgressMode` | |
| `createMcpHandler` (`agents/mcp/server`) + `@tailworks/mcp` `withTailnet` | n/a (Worker) | | `CreateMcpHandlerWithTailnet` | per-request caller; `lookup_order` allowed, `refund` → `isError` with `cap_missing` JSON |
| Agent as MCP client: `addMcpServer(name, url)` + `this.mcp.callTool` | ✅ | ✅ | `AgentAsMCPClient` | to a tailworks MCP server on **another host** over the tailnet. The server's per-request caller is the agent's own node (facet) or the workload node (plain-DO). Allow and deny both checked |
| `addMcpServer` to an MCP server on the **same host** | ✅ | ✅ | `SameHostMCP` | netd delivers a flow to a node on the same host in process (RFC §4.8): the server's netmap, packet filter and WhoIs decide as for a call from another host; no `--allow-sibling-egress` |
| `subAgent()` / sub-agent facets | ❌ | ? | `UnsupportedBindings` | inside a facet the Dynamic Worker's `ctx.exports` has no tenant classes ("Sub-agent class … not found in worker exports"). Use `ctx.tailnet.spawn` (own node, narrower caps) |
| `env.AI` (Workers AI) | ❌ | ❌ | `UnsupportedBindings` | no binding. Use an AI SDK provider pointed at a tailnet LLM gateway (`ai-agent` template, `src/model.ts`) |
| Workers KV binding (incl. `OAUTH_KV` for workers-oauth-provider) | ✅ | ✅ | `TestOAuthProviderKV` | `@cloudflare/workers-oauth-provider` 1.2.4 unmodified: registration, PKCE code flow, refresh, the protected API, `listClients`/`listUserGrants`, replay revocation. See [Workers KV](#workers-kv-details). For tailnet callers `getCaller` is still simpler than OAuth |
| Workflows: `AgentWorkflow`, `runWorkflow`, `getWorkflowStatus`, `step.reportComplete` | ✅ | ✅ | `TestWorkflowApprovals`, `TestRefundApprovalTemplate`, `TestAgentWorkflowCallsBack` | `TestAgentWorkflowCallsBack` also runs on the real mgmt, with Agent classes that have no node of their own: until [#359](https://github.com/rosskukulinski/tailworks/issues/359) such a workflow stayed queued on a deployed host (Cloudflare's `workflows` example, [upstream examples](#upstream-examples-agents-sdk)). On workerd's built-in engine. A failed step retries from itself (completed steps return their stored results); the instance survives a hostd restart. Tailworks keeps `AgentWorkflow` from reporting the engine's end of a long wait (`WorkflowAbortedError`) to the Agent as an error |
| `waitForApproval` | ✅ | ✅ | `TestWorkflowApprovals/ApproveWithIdentity`, `TestWorkflowApprovals/Reject` | resolved only by tailworks ([approvals](#approvals)): the returned metadata carries `approver` (user, node, nodeId, tags, via) and `comment`; a forged `approver` in the request is replaced; a rejection throws `WorkflowRejectedError` with the comment as reason; a timeout rejects |
| `approveWorkflow` / `rejectWorkflow` from app code | 🚫 | 🚫 | `TestWorkflowApprovals/NoGrantRefused` | by design: an approval is a decision by a tailnet identity, so app code may not send one (`approval_requires_identity`); approve with `tw approve`, the admin UI or `POST /_tw/approvals/<workflow>/<instance>` |
| Email (`cloudflare:email`, `routeAgentEmail`) | ❌ | ❌ | `UnsupportedBindings` | the app's own import of `cloudflare:email` is refused by the built-in bundler (`not_supported`); the SDK's own import (under `node_modules`) and pre-built Workers get a stub, and constructing `EmailMessage` throws "not supported on tailworks" |
| Browser Rendering (`env.BROWSER`, `agents/browser`) | ❌ | ❌ | `UnsupportedBindings` | |
| Codemode / dynamic agents (`env.LOADER` for tenants) | ❌ | ❌ | `UnsupportedBindings` | Worker Loader is platform-only |
| `harness/container/*` | not tested | not tested | — | was ❌ (needed a container). Tailworks now implements `ctx.container` (ADR-032, below), the primitive it builds on, but no `harness/container` code has been run on tailworks |
| `McpAgent` | ✅ | | `e2e/compat` (`mcp-agent`, `ag-webmcp`) | Cloudflare's `mcp` and `webmcp` examples ([upstream examples](#upstream-examples-agents-sdk)): tools, and state in the Durable Object across calls. Deprecated upstream; prefer `createMcpHandler` (ADR-022) |
| `@callable` RPC over the agent's WebSocket (what `useAgent` calls), streaming callables | ✅ | | `e2e/compat` (`ag-github-webhook`, `ag-push-notifications`, `ag-workflows`) | from Cloudflare's examples, with a Go client speaking the `rpc` messages; the React `useAgent` hook itself is not run |
| Agents reached through the imported env (`import { env } from "cloudflare:workers"` passed to `routeAgentRequest` / `getAgentByName`) | ⚠️ | | `e2e/compat` (`ag-cross-domain`, `ag-mcp-elicitation`) | works for a class not listed in `tailnet.objects` (internal: no node, so no create admission, [#492](https://github.com/rosskukulinski/tailworks/issues/492)). For a class listed with tags, a new agent name is `403 create_not_admitted` ([create admission](create-admission.md), [#355](https://github.com/rosskukulinski/tailworks/issues/355)); pass the `env` argument of `fetch` instead |
| `AIChatAgent` (`@cloudflare/ai-chat`): `onChatMessage`, `saveMessages`, `get-messages`, server tools, `needsApproval` | ✅ | not tested | `cmd/tw:TestTemplateTests/chat-agent` | the `chat-agent` template under `tw dev`: over HTTP, a server tool runs and a large `calculate` stops at `approval-requested`; over the WebSocket (`useAgentChat`'s protocol, the template's page), an approval runs the tool and a client tool's answer continues the turn; one chat per caller. Every upstream example that uses it needs Workers AI, so the template is its test. `routeAgentRequest` takes the instance name from the path without decoding it, so the template URL-encodes the caller's key everywhere. Not tested: the React `useAgentChat` client itself, stream resume |
| `PiHarness` (`agents/harness/pi`) + `Lifecycle` | ✅ | not tested | `cmd/tw:TestTemplateTests/pi-agent` | the `pi-agent` template under `tw dev`: Pi Durable on a plain `DurableObject` in facet mode, a prompt, a `word_count` tool round, the transcript in the object's SQLite, one assistant per caller. Not tested: the Lifecycle alarm resuming a run after eviction, sessions, signals |

### Upstream examples: Agents SDK

Cloudflare's own examples, from the `examples/` directory of
[cloudflare/agents](https://github.com/cloudflare/agents) (the playground is
one of them), run by [`e2e/compat`](../e2e/compat/README.md). "Unmodified"
is what that README says: the example's source and config as they are at the
pinned commit, installed outside the monorepo, plus a `tailworks.jsonc` with
only a name and a tag. Anything more is in the last column.

<!-- upstream-examples:agents-sdk:begin -->
<!-- Generated by `make compat-upstream` from e2e/compat/upstream-results.json: don't edit this block by hand. -->

Upstream: [cloudflare/agents](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples) `examples/` at commit `95dacf84dcdf`. Versions: `agents` 0.34.0 (the examples depend on `"agents": "*"`, a workspace link; the suite installs the published version the repo carries at that commit, with `npm install`), `@modelcontextprotocol/server` 2.4.0 and `@modelcontextprotocol/sdk` 1.32.2 (pinned by the examples themselves). No other versions were run, so none is known not to work.

- Under `tw dev` (`make e2e-compat-dev`, no tailnet and no credentials): run 2026-11-25 on darwin/arm64, tailworks `d4ac417b9325`.
- Deployed with `tw deploy` to a real host on a real tailnet and called from a tailnet client (`make e2e-compat`): run 2026-11-25 on darwin/arm64, tailworks `d4ac417b9325`.

69 examples: **22 pass, 1 pass with a change, 0 fail, 46 not run.** (The repo has 56 example directories. One of them, `next/`, holds 14 examples of its own, counted here one by one, with `next/harnesses` as one.) A row's result is the worse of its two runs. "Pass" means the suite's probe passed: it does what the example's README or client does (the requests, the WebSocket and `@callable` calls, the MCP sessions), not everything the example can do.

| Example | What it is | `tw dev` | Deployed | Result | The change, or why it fails |
|---|---|---|---|---|---|
| [`mcp-server`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/mcp-server) | Stateless MCP server (createMcpHandler) | pass | pass | **pass** | — |
| [`mcp`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/mcp) | Stateful MCP server on an Agents SDK McpAgent (Durable Object state) | pass | pass | **pass** | — |
| [`cross-domain`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/cross-domain) | Agent behind onBeforeConnect / onBeforeRequest token checks: HTTP and WebSocket | pass | pass | **pass** | — |
| [`github-webhook`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/github-webhook) | GitHub webhook receiver: HMAC check, one agent per repo, SQL event log, state sync | pass | pass | **pass** | — |
| [`mcp-client`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/mcp-client) | Agent as MCP client (addMcpServer, @callable RPC) | pass | pass | **pass** | — |
| [`mcp-elicitation`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/mcp-elicitation) | Stateful MCP server with elicitation (MCP SDK v1 on a Durable Object, WorkerTransport) | pass | pass | **pass** | — |
| [`mcp-elicitation-mrtr`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/mcp-elicitation-mrtr) | Stateless MCP elicitation, multi-round-trip requests (MCP 2026-07-28 only) | pass | pass | **pass** | — |
| [`mcp-worker`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/mcp-worker) | Stateless MCP server in a Worker (createMcpHandler) + React tool tester | pass | pass | **pass** | — |
| [`mcp-worker-authenticated`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/mcp-worker-authenticated) | MCP server behind OAuth 2.1 (@cloudflare/workers-oauth-provider on KV) | pass | not run | **pass with change** | (1) `vars.MCP_RESOURCE` in wrangler.jsonc set to the served URL + `/mcp`, as the example's README says to. (2) Deployed: `"tailnet": {"https": true}` in tailworks.jsonc, so the resource URL is the workload's `https://` tailnet URL ([#360](https://github.com/rosskukulinski/tailworks/issues/360)). |
| [`push-notifications`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/push-notifications) | Reminder agent: @callable RPC, schedule(), state sync, Web Push (VAPID secrets) | pass | pass | **pass** | — |
| [`webmcp`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/webmcp) | McpAgent (MCP SDK v1, state in a Durable Object) + browser WebMCP adapter page | pass | pass | **pass** | — |
| [`webmcp-react`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/webmcp-react) | WebMCP starter: a React SPA (tools run in the browser), Worker only as the 404 fallback | pass | pass | **pass** | — |
| [`workflows`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/workflows) | AgentWorkflow started by an agent: steps, progress reports to the agent and its clients, restart, list. (Its approval step sits behind the example's intentional random failures and isn't reached) | pass | pass | **pass** | — |
| [`playground`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/playground) | The Agents playground: 30 agent classes in one Worker (state, callable, streaming, schedule, SQL, routing, multi-agent, MCP, workflows; AI, email, voice and codemode demos too) | pass | pass | **pass** | — |
| [`next/lifecycle`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/next/lifecycle) | A plain `DurableObject` with `Lifecycle` and a job-queue capability: a self-rescheduling durable job, cancel, `ctx.abort()` restart | pass | not run | **pass** | — |
| [`next/mcp-client`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/next/mcp-client) | `MCPClientManager` as a lifecycle capability on a plain `DurableObject`: register, connect, catalog, remove (+ React UI) | pass | not run | **pass** | — |
| [`next/queue`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/next/queue) | `Queue` as a lifecycle capability: durable background items run from the alarm loop | pass | not run | **pass** | — |
| [`next/routing`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/next/routing) | `RoutedAgents` on a plain `DurableObject` hub: one Agent per chat, forwarded requests and WebSockets, pushed metadata, search, delete (+ React UI) | pass | not run | **pass** | — |
| [`next/schedules`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/next/schedules) | `Scheduler` as a lifecycle capability: a one-shot reminder delivered from the alarm, a cron schedule, cancel | pass | not run | **pass** | — |
| [`next/sessions`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/next/sessions) | `Sessions` on a plain `DurableObject`: a message tree, streamed (NDJSON) history, branches, a compaction overlay | pass | not run | **pass** | — |
| [`next/streams`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/next/streams) | `Streams` with `Tasks`: a durable task appends chunks to a durable stream, read over SSE, resumed from a cursor | pass | not run | **pass** | — |
| [`next/tasks`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/next/tasks) | `Tasks`: a durable replayable run with steps and a durable sleep, idempotency keys, run lookup | pass | not run | **pass** | — |
| [`next/websockets`](https://github.com/cloudflare/agents/tree/95dacf84dcdf6582bc9bdd5fa61c6ed5528ec01c/examples/next/websockets) | The `WebSockets` capability on a plain `DurableObject`: the Agent protocol (`useAgent`), room frames, `rpc` callables incl. a streamed one, tags, HTTP broadcast (+ React UI) | pass | not run | **pass** | — |

Not run:

| Why | Examples |
|---|---|
| needs Workers AI (`env.AI`, an `ai` binding in its wrangler config), which tailworks doesn't provide. A scripted `env.AI` isn't possible without changing each example: tailworks ignores the `ai` binding (`env.AI` is undefined), so there is no binding to point at a stub, and one would have to be wired into each example: its config, or a wrapper around each of its agent classes (they read `this.env.AI` inside their Durable Objects) | `a2a`, `agents-as-tools`, `auth-agent`, `chat-sdk-messenger`, `codemode-browser`, `context-overflow-recovery`, `deploy-churn`, `dynamic-tools`, `mcp-rpc-transport`, `multi-ai-chat`, `resumable-stream-chat`, `structured-input`, `think-chat-sdk`, `think-submissions`, `think-workflows`, `vue-chat`, `web-fetch-lab`, `web-research` |
| needs Workers AI, and a voice or telephony provider's API key | `elevenlabs-starter`, `plivo-voice-agent`, `telnyx-voice-agent`, `voice-agent`, `voice-input` |
| needs Workers AI and Browser Rendering (`browser` binding) | `ai-chat`, `browser-live-view`, `browser-quick-actions`, `codemode-connectors` |
| needs Workers AI and the Worker Loader (`worker_loaders` binding) | `codemode`, `computer-skills`, `r2-skills`, `worker-bundler-playground`, `workspace-chat` |
| needs the Worker Loader (`worker_loaders` binding), which tailworks keeps to the platform | `codemode-mcp`, `codemode-mcp-openapi`, `dynamic-workers`, `dynamic-workers-playground` |
| needs Workers AI and a container (`containers` binding with a Dockerfile); the suite runs the process tier only | `sandbox-coding-agent` |
| needs Email Routing (`send_email` binding, the `email()` handler) | `email-agent` |
| needs an OpenAI API key (the agent's moves come from `@ai-sdk/openai`) | `tictactoe` |
| needs a funded test wallet key and the public x402.org facilitator | `x402`, `x402-mcp` |
| needs Workers AI, the Worker Loader, a container or a model provider key | `next/channels`, `next/dynamic-agents`, `next/harnesses/*`, `next/models`, `next/tools/browser` |

<!-- upstream-examples:agents-sdk:end -->

## Secrets: details

`tw secret put|ls|rm` and `.dev.vars` work like `wrangler secret` and
`wrangler dev`. Your code reads a secret as `env.NAME`, a text binding,
in the Worker and in Durable Objects. For the `node` runtime it is
`process.env.NAME` (loaded by a preload, not passed in the process
environment). See [secrets.md](secrets.md).

There are four differences from Cloudflare:

- A secret **overrides** a var of the same name; Cloudflare doesn't allow
  both.
- A change takes effect on the next `tw deploy`.
- The microVM tier refuses workloads that have secrets
  (`secrets_unsupported_tier`).
- `tw secret bulk` checks the whole input first, then sets the secrets
  one at a time. If one fails, those before it stay set, and the error
  names what was set, what failed and what was not attempted.

Secret versions and the Secrets Store binding are not supported.

## Workers KV: details

`kv_namespaces` bindings work as on Cloudflare: workerd's own
`kvNamespace` binding, so the API and its argument checks are upstream's.
Tailworks supplies the storage: one platform-owned SQLite Durable Object per
namespace on the workload's host (local reads), with every write first
appended to a sealed, lease-fenced journal in mgmt. See [kv.md](kv.md) and
[ADR-037](design/adr/037-workers-kv.md).

Measured (`hostd:TestWorkersKV`, `hostd:TestWorkersKVMove`,
`e2e/vm:TestE2EVMKV`, `e2e/sdk:TestOAuthProviderKV`, macOS arm64):

- every `get` type, `getWithMetadata`, bulk `get` (100 keys max, `text` or
  `json`, with metadata), `put` with `expiration` / `expirationTtl` /
  `metadata` / a stream, `delete`, and `list` with `prefix`, `limit` and
  `cursor`, from the Worker and from a facet;
- Cloudflare's errors and limits: 512-byte keys (`414`), 1024-byte
  metadata, TTL at least 60 s, `limit` at most 1000;
- expiry: a key is gone from `get` and `list` once it expires, and the
  namespace's alarm journals its delete;
- a hostd restart, and a lease move to a second host (whose workerd had no
  DO storage), keep every key; moving back catches up on the writes made
  meanwhile;
- with mgmt's journal unreachable, a restarted host still serves reads
  from its local copy, and writes fail with `503` without landing;
- the microVM tier (Virtualization.framework): first `put` 83 to 222 ms
  over two runs (the namespace object starting, plus a journal append from
  the guest); 20 sequential `get`s took 13 to 14 ms end to end over the
  tailnet.

How it differs from Cloudflare:

- **Scope.** A namespace belongs to one workload, named by its `id` (or the
  binding). Another workload's namespace with the same `id` is a different
  namespace. Cloudflare shares namespaces across an account.
- **Consistency.** A workload reads its own writes. Writes by another
  writer (a replica, `tw kv key put`) are seen within 60 s, or the read's
  `cacheTtl`. Writes are linearizable (the journal orders them).
- **Availability.** A write needs mgmt. When mgmt is down, reads work
  from the local copy after waiting at most 2 s for it, until the host
  stops the workload: after 30 s if it has a Durable Object class not
  marked `"fence": "never"`, else not at all.
- **Values** are at most 2 MiB − 4 KiB (Cloudflare: 25 MiB).
- `cacheStatus` is always `null`.

Not supported: sharing a namespace between workloads, larger values,
`wrangler kv bulk`, and `--remote` / `preview_id` (ignored).

## Cron triggers

`wrangler.jsonc` `triggers.crons` and the default export's
`scheduled(controller, env, ctx)` work as on Cloudflare (UTC, Cloudflare's
cron syntax with 1 = Sunday, `controller.cron` and
`controller.scheduledTime`). See [cron-triggers.md](cron-triggers.md). The
`node` runtime does not support them.

## Queues

`wrangler.jsonc` `queues.producers` and `queues.consumers`, workerd's
`queue` binding and the default export's `queue(batch, env, ctx)` work as on
Cloudflare: batching by size and timeout, `ack`/`retry` per message and per
batch, retry delays, `max_retries` and a dead-letter queue, `delaySeconds`
and all four content types. The broker is mgmt (queues belong to the
tenant), so a consumer on another host, a hostd restart, a crash mid-batch
or a lease move loses nothing (`make e2e-queues`). Differences: several
workloads may consume one queue, `max_concurrency` doesn't autoscale, and
HTTP pull is tailworks' admin API. See [queues.md](queues.md). The `node`
runtime does not support queues.

## Logs, metrics and traces (ADR-045)

See [observability.md](observability.md). Measured by `TestTelemetry`
(`hostd`, hermetic: real workerd, testcontrol, a tsnet caller, echo and
OTLP collector nodes) and `e2e/observability` (tw-mgmt and tw-hostd as
separate processes).

| Cloudflare | tailworks | Test | Notes |
|---|---|---|---|
| `console.*` and uncaught exceptions in `wrangler tail` / Workers Logs | ✅ `tw logs <workload>` | `TestTelemetry/RecordsJoinIngressAndTail`, `ExceptionAndNoQueryString` | one record per request: tailworks' half (verified tailnet caller, status, duration) joined with workerd's tail (console lines with level, exceptions with stack, outcome). The Worker, plain-DO and facet classes alike; a DO called from the Worker is its own record. Stacks are source-mapped to `src/…` when the bundle carries a map ([dev-debugging.md](dev-debugging.md)) |
| `wrangler tail --format json` | ✅ `tw logs --json` | `TestTelemetry/LogsAPI` | JSON Lines, the `RequestRecord` schema |
| `wrangler tail` filters (`--status`, `--method`, `--search`, `--ip`, `--header`, `--sampling-rate`) | ⚠ different | `TestTelemetry/LogsAPI`, `TestRecordFilterTextBound` (`internal/obs`) | `--status 5xx`, `--outcome`, `--errors`, `--object`, `--caller` (tailnet identity, not IP), `--trace`; applied on the host. No `--search`, `--header` or sampling |
| Tail Workers (`tail_consumers`) and streaming tails for your code | ❌ | — | tailworks uses workerd's tails itself (the platform's `Tail` entrypoint); export with OTLP instead |
| Workers Logs / Logpush export; Workers Observability OpenTelemetry export (logs and traces to an OTLP destination) | ✅ OTLP/HTTP (JSON) per tenant | `TestTelemetry/OTLPExportFromTheWorkloadNode`, `e2e/observability` | `tw telemetry set --otlp <url> --header NAME`. Logs, metrics and traces; headers in mgmt custody. The collector is a **tailnet peer** dialed from each workload's own node, so the tailnet policy decides; no public destinations |
| Automatic tracing (Workers Traces): `traceparent` across requests | ⚠ partial | `TestTelemetry/TraceparentAcrossDOAndEgress`, `TestTelemetry/EntryTraces`, `e2e/observability` | spans for tailworks' ingress (SERVER), DO stub `fetch()` (CLIENT, from the runtime), DO RPC `stub.method()` (CLIENT in the caller, SERVER in the object), egress and `ctx.tailnet.fetch` (CLIENT); one trace across caller → Worker → DO → egress → the next hop. Alarms and cron runs start their own trace (a root SERVER span), and their records carry it. Not traced: RPC through a raw binding or to a Worker entrypoint, WebSocket events, queue batches, Workflow steps, storage operations |
| Workers metrics / Analytics (requests, errors, duration) | ✅ Prometheus + OTLP | `TestTelemetry/PrometheusScrape`, `e2e/observability` | `GET /metrics` on port 9464 of each host's and mgmt's own tailnet node (a policy grant decides who scrapes), OpenTelemetry semantic-convention names; per tenant via OTLP |
| CPU time (`cpuTime` in tail events, the CPU time metric) | ❌ | `TestTelemetry` | workerd reports `cpuTime: 0` and `wallTime: 0`; tailworks reports wall time from its ingress |
| Workers Analytics Engine binding | ❌ | — | not a binding on tailworks |

## Vercel AI SDK (`ai` 7.0.177) and `mcp-handler` 2.4.0

| Feature | Status | Test | Notes |
|---|---|---|---|
| `ToolLoopAgent.stream()` in a Durable Object (facet) | ✅ | `ToolLoopAgent/*` | `MockLanguageModelV4` (scripted: tool calls, then text). One agent = one node: the MCP server saw `…-aiagent-<name>` as the caller |
| `ToolLoopAgent.stream()` in the Worker | ✅ | `ToolLoopAgent/*` | caller = the workload node |
| `toUIMessageStreamResponse()` (SSE, what `useChat` reads) | ✅ | `ToolLoopAgent/*` | 36–38 events; first event 50–400 ms after the request on this Mac |
| `@ai-sdk/mcp` `createMCPClient` (HTTP transport) → tailworks MCP server over the tailnet | ✅ with `mcpFetch` | `ToolLoopAgent/*` | `tools()` from both `mcp-handler` and CF `createMcpHandler`; allowed tool output and denied tool (`cap_missing`) reach the model |
| `@ai-sdk/mcp` with the default `fetch` | ❌ | — | its default `redirect: "error"` throws in workerd ("Invalid redirect value"). `@tailworks/mcp` `mcpFetch(ctx)` emulates it (manual + check) |
| `@ai-sdk/mcp` stdio transport | ❌ | — | `child_process` (Node runtime) |
| `mcp-handler` `createMcpHandler` + `withTailnet` | ✅ | `McpHandlerWithTailnet` | Web-standard handler mounted at `/mcp`; per-request caller, allow/deny, `whoami` |
| Typed egress refusals through the MCP client | ✅ | `EgressRefusalIsTyped` | `mcpFetch` throws `TailnetError`; the template renders it (`403 peer_not_visible`) instead of a 500 |
| `traceparent` propagation | ✅ | `TraceparentPropagated`, `TestTelemetry/TraceparentAcrossDOAndEgress` | `mcpFetch` sends `00-<caller traceId>-<span>-01`, and tailworks itself propagates the W3C trace across ingress, DO calls and egress (ADR-045). The **audit** trace ID (`getCaller().traceId`) is still each hop's own: the header is never trusted for audit or identity |
| On-behalf-of (`actingFor`) assertions on MCP calls | ❌ | — | RFC §4.6 not implemented: the server sees the agent's node, not the human it acts for |
| `WorkflowAgent` / Workflow SDK | ❌ | — | `node:vm` (ADR-022: Node runtime, branch m9) |
| `HarnessAgent`, Vercel Sandbox, hosted Vercel services | ❌ | — | client-only or process tier (Cloudflare's Sandbox SDK: see below) |

### Upstream examples: Vercel AI SDK

The examples in [vercel/ai](https://github.com/vercel/ai)'s `examples/`
directory. None of them targets Workers, and all of them call a model
provider with an API key. The three that expose their server without a
framework build (Hono, `node:http`, Express) run here as Workers, with a
scripted stand-in for the OpenAI API, so these rows show the AI SDK's
streaming and agent loop running on tailworks, not a model.

<!-- upstream-examples:vercel-ai:begin -->
<!-- Generated by `make compat-upstream` from e2e/compat/upstream-results.json: don't edit this block by hand. -->

Upstream: [vercel/ai](https://github.com/vercel/ai/tree/8956ca55ed9c31b4b7680621a10ad0269f9629b1/examples) `examples/` at commit `8956ca55ed9c`. Versions: `ai` 7.0.181 and `@ai-sdk/openai` 4.0.122 (the examples depend on `workspace:*`; the suite installs the published versions the repo carries at that commit). No other versions were run.

- Under `tw dev` (`make e2e-compat-dev`, no tailnet and no credentials): run 2026-11-25 on darwin/arm64, tailworks `d4ac417b9325`.
- Deployed with `tw deploy` to a real host on a real tailnet and called from a tailnet client (`make e2e-compat`): run 2026-11-25 on darwin/arm64, tailworks `d4ac417b9325`.

26 examples: **0 pass, 4 pass with a change, 3 fail, 19 not run.** (`mcp` is one directory of MCP server and client script pairs; two of its servers run here, one row each, so it is counted twice.) A row's result is the worse of its two runs. "Pass" means the suite's probe passed: it does what the example's README or client does (the requests, the WebSocket and `@callable` calls, the MCP sessions), not everything the example can do.

| Example | What it is | `tw dev` | Deployed | Result | The change, or why it fails |
|---|---|---|---|---|---|
| [`hono`](https://github.com/vercel/ai/tree/8956ca55ed9c31b4b7680621a10ad0269f9629b1/examples/hono) | Hono server: `streamText`, text and UI message streams, a `ToolLoopAgent` behind `createAgentUIStreamResponse` | pass | pass | **pass with change** | (1) The example is a Node server with no Workers config: a wrangler.jsonc and a Worker entry are added, and `@hono/node-server` is aliased to a shim whose `serve()` hands Hono's `app.fetch` to that entry instead of listening on port 8080; `src/` is unchanged. (2) No OpenAI key: the entry answers the example's OpenAI Responses API calls with a scripted stream, so no model is called. |
| [`node-http-server`](https://github.com/vercel/ai/tree/8956ca55ed9c31b4b7680621a10ad0269f9629b1/examples/node-http-server) | `node:http` server: `streamText` piped with `pipeUIMessageStreamToResponse` | pass | pass | **pass with change** | (1) The example is a Node server with no Workers config: a wrangler.jsonc and a 4-line Worker entry (`httpServerHandler({ port: 8080 })` from `cloudflare:node`) are added; `src/` is unchanged. (2) No OpenAI key: the entry answers the example's OpenAI Responses API calls with a scripted stream, so no model is called. |
| [`express`](https://github.com/vercel/ai/tree/8956ca55ed9c31b4b7680621a10ad0269f9629b1/examples/express) | Express 5 server: `streamText`, and a `ToolLoopAgent` piped with `pipeAgentUIStreamToResponse` | pass | pass | **pass with change** | (1) The example is a Node server with no Workers config: a wrangler.jsonc and a 4-line Worker entry (`httpServerHandler({ port: 8080 })` from `cloudflare:node`) are added; `src/` is unchanged. (2) No OpenAI key: the entry answers the example's OpenAI Responses API calls with a scripted stream, so no model is called. |
| [`fastify`](https://github.com/vercel/ai/tree/8956ca55ed9c31b4b7680621a10ad0269f9629b1/examples/fastify) | Fastify 5 server: `streamText` as a UI message stream, and `createUIMessageStream` with custom data | fail | not run | **fail** | Not a tailworks gap: Fastify doesn't run as a Worker. It starts, but `fastify.listen()` never reaches the node:http bridge (`Http server with port 8080 not found`), and stock `wrangler dev` doesn't start it at all (its bundle of Fastify's pino logger throws `Cannot destructure property 'serializersSym' of 'pino.symbols'`) |
| [`nest`](https://github.com/vercel/ai/tree/8956ca55ed9c31b4b7680621a10ad0269f9629b1/examples/nest) | Nest server (Express platform): `streamText` piped with `pipeUIMessageStreamToResponse`, and custom data | fail | not run | **fail** | Not a tailworks gap: Nest `require()`s its optional packages (`@nestjs/microservices`, `@nestjs/websockets`, `class-validator`, `class-transformer`) in code a bundler can't leave out, so the Worker doesn't bundle: `Could not resolve "@nestjs/microservices"`. Stock `wrangler dev` fails the same way |
| [`mcp`](https://github.com/vercel/ai/tree/8956ca55ed9c31b4b7680621a10ad0269f9629b1/examples/mcp) | `mcp`'s `http` server: an Express MCP server on the Streamable HTTP transport (MCP SDK v1, stateless), the one its `client:http` calls | pass | not run | **pass with change** | The example is a Node server with no Workers config: a wrangler.jsonc and a 4-line Worker entry (`httpServerHandler({ port: 3000 })` from `cloudflare:node`) are added; `src/` is unchanged |
| [`mcp`](https://github.com/vercel/ai/tree/8956ca55ed9c31b4b7680621a10ad0269f9629b1/examples/mcp) | `mcp`'s `sse` server: an Express MCP server on the legacy SSE transport (`GET /sse`, `POST /messages`), the one its `client:sse` calls | fail | not run | **fail** | Not a tailworks gap: the server keeps the `GET /sse` response in a module variable and writes each answer to it while handling a later `POST /messages`, and a Worker can't write to one request's response from another (stock `wrangler dev` cancels the request the same way: the stream gets the endpoint event, the `POST` gets 202, no answer arrives). Its `http` server, the Streamable HTTP transport, passes |

Not run:

| Why | Examples |
|---|---|
| a Next.js, Nuxt, SvelteKit or Angular app that calls a model provider with an API key. tailworks' Worker tier runs Workers projects, not these frameworks' builds, and none of them has a Workers target | `ai-e2e-next`, `angular`, `harness-e2e-next`, `next`, `next-agent`, `next-fastapi`, `next-google-vertex`, `next-langchain`, `next-openai-kasada-bot-protection`, `next-openai-pages`, `next-openai-telemetry`, `next-openai-telemetry-sentry`, `next-openai-upstash-rate-limits`, `next-workflow`, `nuxt-openai`, `sveltekit-openai`, `xai-tts-demo` |
| scripts and a terminal UI, not servers; they call model providers with API keys | `ai-functions`, `harness-e2e-tui` |

<!-- upstream-examples:vercel-ai:end -->

### Upstream examples: mcp-handler

The three examples in [vercel/mcp-handler](https://github.com/vercel/mcp-handler)'s
`examples/` directory.

<!-- upstream-examples:mcp:begin -->
<!-- Generated by `make compat-upstream` from e2e/compat/upstream-results.json: don't edit this block by hand. -->

Upstream: [vercel/mcp-handler](https://github.com/vercel/mcp-handler/tree/5518e8af4386a92756d189169a97601a4a7162ee/examples) `examples/` at commit `5518e8af4386`. Versions: `mcp-handler` 2.4.0 (the repo's version at that commit), `@modelcontextprotocol/server` 2.4.0, `zod` 4.6.7. No other versions were run.

- Under `tw dev` (`make e2e-compat-dev`, no tailnet and no credentials): run 2026-11-25 on darwin/arm64, tailworks `d4ac417b9325`.
- Deployed with `tw deploy` to a real host on a real tailnet and called from a tailnet client (`make e2e-compat`): run 2026-11-25 on darwin/arm64, tailworks `d4ac417b9325`.

3 examples: **0 pass, 3 pass with a change, 0 fail, 0 not run.** A row's result is the worse of its two runs. "Pass" means the suite's probe passed: it does what the example's README or client does (the requests, the WebSocket and `@callable` calls, the MCP sessions), not everything the example can do.

| Example | What it is | `tw dev` | Deployed | Result | The change, or why it fails |
|---|---|---|---|---|---|
| [`route.ts`](https://github.com/vercel/mcp-handler/blob/5518e8af4386a92756d189169a97601a4a7162ee/examples/route.ts) | `createMcpHandler`, one tool | pass | pass | **pass with change** | The example is a route module (`export { handler as GET, handler as POST }`), not a project: a package.json, a wrangler.jsonc and a 10-line Worker entry that mounts it at `/api/mcp` are added; `route.ts` is unchanged |
| [`auth/route.ts`](https://github.com/vercel/mcp-handler/blob/5518e8af4386a92756d189169a97601a4a7162ee/examples/auth/route.ts) | `withMcpAuth`: bearer token with required scopes | pass | pass | **pass with change** | The example is a route module (`export { handler as GET, handler as POST }`), not a project: a package.json, a wrangler.jsonc and a 10-line Worker entry that mounts it at `/api/mcp` are added; `route.ts` is unchanged |
| [`auth-cookie/route.ts`](https://github.com/vercel/mcp-handler/blob/5518e8af4386a92756d189169a97601a4a7162ee/examples/auth-cookie/route.ts) | `withMcpAuth`: same-origin session cookie, or bearer token | pass | pass | **pass with change** | The example is a route module (`export { handler as GET, handler as POST }`), not a project: a package.json, a wrangler.jsonc and a 10-line Worker entry that mounts it at `/api/mcp` are added; `route.ts` is unchanged |

<!-- upstream-examples:mcp:end -->

### Upstream examples: Python Workers

Every example in Cloudflare's
[python-workers-examples](https://github.com/cloudflare/python-workers-examples)
repository ([Python Workers](#python)).

<!-- upstream-examples:python-workers:begin -->
<!-- Generated by `make compat-upstream` from e2e/compat/upstream-results.json: don't edit this block by hand. -->

Upstream: [cloudflare/python-workers-examples](https://github.com/cloudflare/python-workers-examples/tree/47ec7d69f4052cd57bed5d3676cb7af76ca0656b), every top-level directory, at commit `47ec7d69f405`. Versions: the Pyodide bundles the pinned workerd runs (`internal/pyodide`); no package is vendored (`python_modules/`), so an example that needs one is not run. No other versions were run.

- Under `tw dev` (`make e2e-compat-dev`, no tailnet and no credentials): run 2026-11-25 on darwin/arm64, tailworks `d4ac417b9325`.
- Deployed with `tw deploy` to a real host on a real tailnet and called from a tailnet client (`make e2e-compat`): run 2026-11-25 on darwin/arm64, tailworks `d4ac417b9325`.

28 examples: **14 pass, 0 pass with a change, 3 fail, 11 not run.** A row's result is the worse of its two runs. "Pass" means the suite's probe passed: it does what the example's README or client does (the requests, the WebSocket and `@callable` calls, the MCP sessions), not everything the example can do.

| Example | What it is | `tw dev` | Deployed | Result | The change, or why it fails |
|---|---|---|---|---|---|
| [`hello`](https://github.com/cloudflare/python-workers-examples/tree/47ec7d69f4052cd57bed5d3676cb7af76ca0656b/hello) | Python Worker: fetch handler | pass | pass | **pass** | — |
| [`binding`](https://github.com/cloudflare/python-workers-examples/tree/47ec7d69f4052cd57bed5d3676cb7af76ca0656b/binding) | Python Worker: KV put and get | pass | pass | **pass** | — |
| [`durable-objects`](https://github.com/cloudflare/python-workers-examples/tree/47ec7d69f4052cd57bed5d3676cb7af76ca0656b/durable-objects) | Python Durable Object: RPC methods, storage | pass | pass | **pass** | — |
| [`chatroom`](https://github.com/cloudflare/python-workers-examples/tree/47ec7d69f4052cd57bed5d3676cb7af76ca0656b/chatroom) | Python Durable Object: hibernatable WebSocket chatroom, an HTML file read from disk | pass | pass | **pass** | — |
| [`opengraph`](https://github.com/cloudflare/python-workers-examples/tree/47ec7d69f4052cd57bed5d3676cb7af76ca0656b/opengraph) | Python Worker: HTMLRewriter over an internet fetch | pass | pass | **pass** | — |
| [`cron`](https://github.com/cloudflare/python-workers-examples/tree/47ec7d69f4052cd57bed5d3676cb7af76ca0656b/cron) | Python Worker: cron trigger every minute | pass | pass | **pass** | — |
| [`workflows`](https://github.com/cloudflare/python-workers-examples/tree/47ec7d69f4052cd57bed5d3676cb7af76ca0656b/workflows) | Python Workflow | fail | fail | **fail** | Python Workflows aren't supported yet (docs/python.md#limits) |
| [`assets`](https://github.com/cloudflare/python-workers-examples/tree/47ec7d69f4052cd57bed5d3676cb7af76ca0656b/assets) | Python Worker serving static assets through env.ASSETS | pass | pass | **pass** | — |
| [`js-api-pygments/ts`](https://github.com/cloudflare/python-workers-examples/tree/47ec7d69f4052cd57bed5d3676cb7af76ca0656b/js-api-pygments/ts) | a TypeScript Worker calling a Python Worker's RPC method (Pygments) over a service binding | fail | pass | **fail** under `tw dev` (pass deployed) | Under `tw dev`: `tw dev` runs one Worker and has no binding to another workload, so env.PYTHON_RPC reaches nothing |
| [`url-shortener`](https://github.com/cloudflare/python-workers-examples/tree/47ec7d69f4052cd57bed5d3676cb7af76ca0656b/url-shortener) | Python Worker on KV, compatibility_date 2026-09-01 (the vendored workers SDK) | pass | pass | **pass** | — |
| [`fastapi`](https://github.com/cloudflare/python-workers-examples/tree/47ec7d69f4052cd57bed5d3676cb7af76ca0656b/fastapi) | FastAPI + Jinja2 (pydantic-core, a compiled Pyodide wheel) | pass | pass | **pass** | — |
| [`django`](https://github.com/cloudflare/python-workers-examples/tree/47ec7d69f4052cd57bed5d3676cb7af76ca0656b/django) | Django | pass | pass | **pass** | — |
| [`django-markdown-r2`](https://github.com/cloudflare/python-workers-examples/tree/47ec7d69f4052cd57bed5d3676cb7af76ca0656b/django-markdown-r2) | Django + Markdown, a Durable Object and an R2 bucket | pass | pass | **pass** | — |
| [`django-todo-d1`](https://github.com/cloudflare/python-workers-examples/tree/47ec7d69f4052cd57bed5d3676cb7af76ca0656b/django-todo-d1) | Django Todo-Backend on D1 (django-cf), D1 migrations | pass | pass | **pass** | — |
| [`mcp-server`](https://github.com/cloudflare/python-workers-examples/tree/47ec7d69f4052cd57bed5d3676cb7af76ca0656b/mcp-server) | MCP server (the Python MCP SDK) on D1, Python 3.14 | pass | fail | **fail** deployed (pass under `tw dev`) | Deployed: the example's MCP app keeps the Python MCP SDK's default host 127.0.0.1, which turns on DNS-rebinding protection allowing only localhost Host headers, so a call by the deployed name gets 421 "Invalid Host header" (a workers.dev name would too); under `tw dev` the Host is 127.0.0.1 |
| [`image-gen`](https://github.com/cloudflare/python-workers-examples/tree/47ec7d69f4052cd57bed5d3676cb7af76ca0656b/image-gen) | Pillow image generation (a compiled Pyodide wheel) | pass | pass | **pass** | — |
| [`sync-http-clients`](https://github.com/cloudflare/python-workers-examples/tree/47ec7d69f4052cd57bed5d3676cb7af76ca0656b/sync-http-clients) | requests, urllib3 and httpx.Client fetching the internet | pass | pass | **pass** | — |

Not run:

| Why | Examples |
|---|---|
| needs Workers AI (an `ai` binding), which tailworks doesn't provide | `image-redraw`, `langchain`, `vectorize-rag`, `workers-ai` |
| needs Hyperdrive | `hyperdrive-batched-counter` |
| needs the Worker Loader (`worker_loaders` binding), which tailworks keeps to the platform | `dynamic-py-py` |
| needs its database seeded after the deploy (`d1 execute --file db_init.sql`), which `tw dev` runs can't do; run by `make e2e-compat` as `py-query-d1`, `py-fastapi-todo` and `py-flask-todo` | `fastapi-todo`, `flask-todo`, `query-d1` |
| a long-running Durable Object on the public Bluesky firehose (an outbound WebSocket); not tried | `websocket-stream-consumer` |
| the repo's test harness, not an example | `tests` |

<!-- upstream-examples:python-workers:end -->

## Containers: `ctx.container` and the Cloudflare Sandbox SDK (ADR-032)

Measured by `make e2e-container` (Firecracker, Lima `tw-dev`, arm64),
`make e2e-vz-container` (vz, this Mac) and `make e2e-container-registry`
(the examples built from their own Dockerfiles through tailworks' registry), on
branch `design/containers-sandboxes-microvm-tier` (2026-11-14). Each
container is its own microVM attached to one Durable Object; see
[containers.md](containers.md).

| Thing | Linux arm64 (Firecracker) | Linux amd64 (Firecracker) | macOS (vz) | Process-only / no-KVM hosts | Test |
|---|---|---|---|---|---|
| `ctx.container` in a DO (`start`, `running`, `inspect`, `exec`, `getTcpPort().fetch`, `monitor`, `signal`, `destroy`, `setInactivityTimeout`) | ✅ | ⚠ not verified | ✅ | ❌ `tier_unavailable` | `TestE2EContainer` `B_raw_ctx_container_python_Files`, `Lifecycle`, `PortsAndAttribution`, `InactivityTimeout`, `KillCascades` |
| `interceptOutboundHttp` / `interceptAllOutboundHttp` / `interceptOutboundHttps` | ✅ enforced | ⚠ | ✅ | ❌ | `Egress/InterceptToParentIdentity`, `Egress/InterceptHTTPS` (e2e), `TestContainerEgress` (hostd) |
| `enableInternet` | ✅ `tailnet.egress.internet` (`true` or an allowlist; absent: none), host's own network | ⚠ | ✅ | ❌ | `Egress/AllowlistedInternet` |
| `getTcpPort().connect()`, WebSocket to a container port | ❌ deferred | ❌ | ❌ | ❌ | |
| `snapshotContainer`, `containerSnapshot`, directory snapshots | ❌ v1.1 / deferred | ❌ | ❌ | ❌ | |
| `@cloudflare/containers` `Container` class (0.3.x) | ✅ (what Sandbox 0.x runs on) | ⚠ | ✅ | ❌ | `A_SandboxSDK0x_minimal` |
| Sandbox SDK 0.x (`@cloudflare/sandbox` 0.12.14) `examples/minimal`: `getSandbox`, `exec`, `writeFile`/`readFile` | ✅ unmodified, arm64 rebuild of `cloudflare/sandbox:0.12.14` via the alias map | ⚠ stock image, not verified | ✅ stock amd64 image under Rosetta (`-vm-rosetta`), or the arm64 rebuild | ❌ | `A_SandboxSDK0x_minimal` |
| Sandbox SDK 1.0 (`@cloudflare/sandbox` 1.0.x): raw `ctx.container.start` + `exec().output()` + `Files` via `sandbox-shim` | ✅ unmodified | ⚠ | ✅ | ❌ | `B_SandboxSDK10_minimal`, `B_raw_ctx_container_python_Files` |
| Sandbox SDK processes, sessions, code interpreter, streaming | not tested | not tested | not tested | ❌ | they use the same `exec` / port paths (assumption) |
| Sandbox SDK `exposePort` / preview URLs (`proxyToSandbox`) | ❌ `not_supported` / `501` (ADR-029) | ❌ | ❌ | ❌ | `TestContainerEgress` (hostd: facet and plain-DO) |
| Sandbox SDK backups, bucket mounts (R2) | ❌ | ❌ | ❌ | ❌ | |
| Images from the app's own Dockerfile via `tw deploy` (built on the developer machine, pinned by digest, pulled by the host) | ✅ | ⚠ (pinning by arch is unit-tested) | not run end to end (host pull uses the same code) | ❌ | `TestE2EContainerRegistry` |

amd64 is "⚠ not verified": the code and the amd64 guest assets exist, and
the amd64 microVM tier itself passed `make e2e-vm` and snapshot restore on a
nested KVM test host on 2026-11-22
([details](install-testing.md#the-microvm-tier)), but `make e2e-container`
has not run on amd64 yet.

## Workers platform config

| Cloudflare | tailworks | Test | Notes |
|---|---|---|---|
| Smart Placement `placement: {"mode": "smart"}` | ✅ different signal | `TestScoreHosts`, `TestAPIPlacement`, `e2e/placement`, multimgmt S12 | Cloudflare moves a Worker toward its backend by analysing request durations per location (up to 15 min after deploy, `SUCCESS` / `INSUFFICIENT_INVOCATIONS`). Tailworks places a workload **at deploy** on the host with the best **measured tailnet path** to what it connects to (`tailnet.connects`, or `placement.targets`): direct WireGuard > peer relay > DERP > unmeasured, then RTT; free capacity breaks ties. Smart is the default when `connects` is set; `"mode": "off"` turns it off. Read from `tailworks.jsonc`, or from `wrangler.jsonc` `placement` if `tailworks.jsonc` has none. ADR-033 (Proposed) |
| `placement.host` / `placement.hostname` (probe targets) | ✅ as one target | `TestResolvePlacement` | the name (port dropped) is resolved in the tenant's netmap (MagicDNS name, host name or IP); Cloudflare probes it from public IPs with TCP/HTTP, tailworks with disco pings from the tenant's own nodes |
| `placement.region` (`aws:us-east-1`) | ⚠ ignored, warned | `TestResolvePlacement` | there are no cloud regions; the deploy's `placement.summary` says so |
| Placement status (`cf-placement` header, `placement_status`) | ⚠ different | `TestAPIPlacement` | `tw deploy` prints why (`placed on h2: tag:db direct 1.8 ms (h1: relayed 24 ms, h3: unmeasured)`); `GET /v1/deployments/{id}` `placement` and `tw ls` show it; the deploy audit record carries it. No per-request header |
| Re-placement while running | ❌ by design | `TestAPIPlacement`, `e2e/placement` | Cloudflare re-routes continuously. Tailworks never moves a running workload on its own (the lease holder is sticky); `tw deploy --replace` re-places it explicitly (hysteresis: a better path class, or ≥ 2 ms and ≥ 20 % lower RTT) |
| Per-object placement (DO location hints, `locationHint`) | ❌ | — | every Durable Object instance of a workload runs on the workload's host |
| Experimental `compatibility_flags` <a id="experimental-flags"></a> (`unsafe_module`, `rtti_api`, `precise_timers`, `experimental`, … ; the flags upstream marks `$experimental`, listed in `internal/compatflags`) | 🚫 refused by default | `hostd/gen:TestExperimentalFlagsRefused`, `internal/compatflags:TestExperimentalRefusedByWorkerd`, `internal/compatflags:TestExperimentalMatchesUpstream`, `mgmt:TestDeployRefusesExperimentalFlags` | Cloudflare never runs them for customers. tailworks runs every workerd with `--experimental` for its own platform worker, which would unlock them for tenant code, so a deploy that lists one is refused with `experimental_flag` ([errors.md](errors.md#experimental_flag)): by mgmt at the deploy and its dry run (`tw deploy`'s preflight), from the known list, and by the host when it generates the config, from the list and from the pinned workerd itself (asked without `--experimental`), so a flag a workerd bump adds is caught too. **What a tenant may set:** any flag the pinned workerd accepts without `--experimental` (`nodejs_compat`, `nodejs_compat_v2`, `nodejs_als`, the dated behaviour flags, …); tailworks adds `nodejs_als` ([Logs, metrics and traces](#logs-metrics-and-traces-adr-045)). **Operator switch:** `tw up --allow-experimental-flags` / `tw-hostd -allow-experimental-flags` lets a host run them (it advertises the `experimental-flags` feature, and mgmt places such deployments only there). A facet object's Dynamic Worker refuses them regardless (workerd's worker loader doesn't inherit `--experimental`), so with the switch they take effect in the Worker and plain-DO objects only. Issue #337, ledger `H-compat-flags` |

## Wrangler config: details

Measured with `e2e/wrangler/testdata/toml-app` (a `wrangler.toml`-only
project) and wrangler 4.60.0 (`make e2e-wrangler`). See
[wrangler.md](wrangler.md).

| Cloudflare / wrangler | tailworks | Tests | Notes |
|---|---|---|---|
| A Workers project with no `tailworks.jsonc` | ✅ defaults | `cmd/tw:TestLoadAppWranglerTOMLOnly`, `cmd/tw:TestCLIDeployWranglerTOML` | workload tag `tag:tw-wl-<workload>`; Durable Object classes internal (`"node": false`), reached through their binding as on Cloudflare |
| `nodejs_compat` through either bundler | ✅ | `e2e/wrangler:TestDevWrangler` | built-in: bare built-ins become `node:*` and workerd provides them, and a CommonJS dependency's `require()` of one becomes an import (`cmd/tw:TestDevRequireBuiltins`); wrangler also inlines unenv polyfills (toml-app's entry: 21 KB against 1.0 KB) |
| `minify`, `keep_names`, `jsx_*`, `tsconfig` | ⚠️ mapped, not measured | — | passed to esbuild's options |
| CommonJS modules, `rules` of type ESModule / CommonJS, Python modules | ❌ | `cmd/tw:TestReadWranglerOutEntry`, `internal/esbundle:TestBuildAppUnsupportedRule` | tailworks ships ES modules (plus text, data, wasm, json) |
| `routes`, `route`, `workers_dev`, `preview_urls`, `account_id` | 🚫 warned | `cmd/tw:TestWranglerWarnings`, `cmd/tw:TestCLIDeployWranglerTOML` | no Cloudflare zone: a workload answers at its MagicDNS name; internet exposure is `tailworks.jsonc` `public` (Funnel) |
| Binding sections with no tailworks binding (`r2_buckets`, `d1_databases`, `services`, `queues`, `ai`, …) | ❌ warned | `cmd/tw:TestWranglerWarnings` | one line per section naming each `env.X` that will be undefined. A section registered as a [platform store](design/adr/044-platform-stores.md) is not warned (`cmd/tw:TestWranglerWarningsStoreKinds`) |
| `assets` | ⚠️ | `cmd/tw:TestAddAssets`, `hostd/gen:TestAssetsAutoTrailingSlash`, `hostd/gen:TestAssetsRedirects` | served by a generated router Worker ([assets](#assets)), with `_redirects`, `_headers` and assets-only projects |
| `pages_build_output_dir` (a Pages project) | ⚠️ | `cmd/tw:TestPagesFunctionsBuild`, `hostd/gen:TestPagesFunctionsRouting` | becomes `assets.directory`; `functions/` or `_worker.js` is compiled into the Worker ([pages](#pages)) |
| `site` (Workers Sites) | ❌ warned | `cmd/tw:TestWranglerWarnings` | deprecated by Cloudflare; use `assets` |
| `upload_source_maps` | ✅ | `cmd/tw:TestWantSourceMap`, `cmd/tw:TestDevParity` | the built-in esbuild ships `dist/index.js.map` (positions and names, no sources) unless it is `false`; hosts map exception stacks with it ([dev-debugging.md](dev-debugging.md)). `tw dev` always maps. With `--wrangler` it is warned: wrangler-built bundles ship no map |
| `observability`, `logpush`, `limits`, unknown keys | ❌ ignored, warned | `cmd/tw:TestWranglerWarnings` | nothing is ignored silently |
| `wrangler dev` parity: source-mapped stacks, `req.url`, `--inspector-port`, hotkeys | ✅ | `cmd/tw:TestDevParity`, `internal/srcmap:TestLookupEsbuild`, `internal/obs:TestFoldRuntimeFrames`, `cmd/tw:TestDevInspectorProxy`, `cmd/tw:TestDevHotkeys`, `cmd/tw:TestDevConsole`, `cmd/tw:TestDevConsoleLive` | uncaught exceptions point at `src/…`, with tailworks' `__tw_rt.js` frames folded into one line; `req.url` keeps the `127.0.0.1:<port>` (or `localhost:<port>`) host you used, not the dev node's IP (identity is WhoIs of the dev node, never `Host`); `--inspector-port` for Chrome DevTools and VS Code; `b` opens a browser, `e` opens a read-only data console (KV keys and values, D1 tables and rows; `--console-port`), `x`/`q` stops. No `d` (DevTools) or `c` (clear) hotkey. [dev-debugging.md](dev-debugging.md) |
| `wrangler dev` / Miniflare as the inner loop | ⚠️ no tailnet | manual (`hello` template) | runs, but `getCaller` throws `no_identity` (fail closed), egress leaves from the developer's own network, `tailnet.objects` is ignored, there is no `ctx.tailnet`. Use `tw dev` for anything tailnet-real |

## Public routes (Funnel, ADR-029)

Cloudflare Workers are public by default; tailworks workloads are tailnet-only
unless `tailworks.jsonc` lists `public.routes` ([public-ingress.md](public-ingress.md)).

| Cloudflare expectation | On tailworks public routes |
|---|---|
| `request.headers.get("CF-Connecting-IP")` | ✅ the client IP Funnel reports (client copies stripped) |
| `X-Forwarded-For` / `X-Forwarded-Proto` | ✅ single value / `https` (set by netd) |
| `request.url` is the `https://` URL the client called | ✅ `https://<workload>.<tailnet>.ts.net/…` (`http://` before [#360](https://github.com/rosskukulinski/tailworks/issues/360)) |
| `request.cf.country`, `asn`, `city`, `colo`, `latitude`… | ❌ absent (`undefined`): public requests aren't geolocated either ([`request.cf`](wrangler.md#requestcf)) |
| Caller identity | `getRequester(req)` → `{kind:"public", ip}`; `getCaller(req)` throws `public_request` |
| `env.NS.getByName(newName)` from a public request | ❌ `public_create_denied` unless the class sets `"public": {"create": true, "maxObjects": N}` |
| `env.NS.getByName(newName)` through a raw binding (`import { env } from "cloudflare:workers"`, a cached `env`) | ❌ `create_not_admitted` for a new name, from any request; existing objects work. Create through the handler's `env` ([create admission](create-admission.md)) |
| Custom domains, bring-your-own front door | ❌ `*.ts.net` only (Funnel) |
| `email()` handler | ❌ (Funnel has no port 25; use a provider's inbound webhook to a public route) |

## tailworks behaviour found by this suite

- **Facet alarms** (fixed): workerd throws "Facets currently cannot set
  alarms", and the Agents SDK's lifecycle re-arms on every wake, so calling
  `schedule()` in facet mode wedged the agent (every later request 500).
  tailworks now delegates facet alarms to the Host DO (`hostd/gen/js/platform.js`
  `__tw_alarm_op`/`alarm`, `@tailworks/tailnet` runtime `installAlarmShim`).
- **Cross-workload object collision** (fixed in M7): M5 found that mgmt
  keyed objects by `(tenant, namespace = class, name)`, so two workloads of
  one tenant with the same DO class and object name got the **same** object
  (the second app's egress left from the first app's node), and hostd
  failed closed with `409 namespace_conflict`. mgmt now scopes identity by
  `(tenant, workload, class, name)`: the two get distinct objects, nodes,
  hostnames and state dirs (`SameClassAcrossWorkloadsDistinctIdentities`).
  hostd keeps `namespace_conflict` as a defensive assertion only.
- **Same-host MCP** (fixed): an agent and the MCP server it called had to
  be on different hosts, because netd refused sibling hairpins. netd now
  delivers a same-host flow in process, after the same netmap and packet
  filter checks (`SameHostMCP`).
- **Class-instance default exports** (fixed): `export default new
  OAuthProvider(...)` keeps `fetch()` on the prototype, and tailworks' runtime
  copied only own properties, so the Worker had no fetch handler. It now
  finds the handler methods (`fetch`, `scheduled`, `queue`, …) along the
  prototype chain (`@tailworks/tailnet` runtime `wrapHandler`).
- **`request.url` is `http://` unless you turn on `tailnet.https`**
  ([#360](https://github.com/rosskukulinski/tailworks/issues/360)):
  tailworks hands a Worker the URL it was reached at. By default that is
  plain HTTP inside the tailnet, `http://<name>.<tailnet>.ts.net/…`
  (WireGuard encrypts it). A request on a [public route](#public-routes)
  came over HTTPS (Funnel), so its `request.url` is `https://` and it
  carries `X-Forwarded-Proto: https`, with or without the setting.
  Code that insists on an HTTPS origin, such as workers-oauth-provider's
  `resourceMetadata.resource` (the token audience), can run unmodified
  with `"tailnet": {"https": true}` in tailworks.jsonc: the workload node
  is also served on `https://<name>.<tailnet>.ts.net` with its ts.net
  certificate, and requests that arrive that way have an `https://`
  `request.url` and `X-Forwarded-Proto: https`. Plain `http://` keeps
  working alongside (it is not redirected). The tailnet needs HTTPS
  certificates; without them the deploy fails with
  [`tailnet_https_unavailable`](errors.md#tailnet_https_unavailable)
  ([tailworks-jsonc.md](tailworks-jsonc.md#tailnethttps)). Without the
  setting, present the canonical origin yourself:
  `e2e/sdk/apps/oauthkv` shows how.

<a id="node-process-tier-m9"></a>
## Node runtime (M9)

See [node-tier.md](node-tier.md) for the full table and the security
caveat: on Linux with Landlock ABI ≥ 4 (kernel 6.7+) and on macOS (Seatbelt)
the sandbox lets a Node app open TCP only to its own port and its egress
proxy, so egress through netd is **enforced**; on Landlock ABI < 4 (and
under `tw-hostd -sandbox seatbelt-legacy`) it is **cooperative** (a malicious Node app can bypass the proxy; `tw status`
says `egress unenforced` and `tw doctor` warns). A multi-tenant host
refuses a Node deploy by default only when it has no process sandbox at
all; see [security-model.md](security-model.md#when-a-host-is-shared-between-tenants).
The microVM tier is the strongest boundary.

| env | value |
|---|---|
| `TW_EGRESS_PROXY`, `HTTP_PROXY`, `HTTPS_PROXY` (+ lower case) | `http://tw:<token>@127.0.0.1:<port>` |
| `NODE_USE_ENV_PROXY` | `1` (Node's `fetch()` and `http` honour the proxy vars) |
| `NO_PROXY` | only the app's own port (self-calls, e.g. the Workflow queue) and the proxy itself |
| Thing | Version | Status on the Node runtime |
|---|---|---|
| Node.js | 25.1 (macOS), 24.x (Lima) | ✅ (needs ≥ 24 for `NODE_USE_ENV_PROXY`) |
| `workflow` (Vercel Workflow SDK) | 5.6.1 | ✅ `"use workflow"`, steps, `sleep`, `createHook`/`resumeHook`, kill/resume |
| `@workflow/world-local` | 5.4.1 | ✅ (single process per data dir) |
| `@workflow/world-postgres` | 5.1.1 | not tested (follow-up) |
| `@ai-sdk/workflow` `WorkflowAgent` | 2.0.109 (`ai` 7.0.177) | ✅ `generate()` with tools as steps, mock `LanguageModelV4` |
| `undici` | 8.12.4 | ✅ via `dispatcher(undici)` / `installGlobalDispatcher(undici)`; ⚠ importing it disables env proxies |
| Node built-in `fetch` / `http` with env proxies | Node 25 | ✅ (CONNECT / absolute form) |
