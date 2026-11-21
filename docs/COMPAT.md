# Cloudflare Workers compatibility

What of the Cloudflare Workers platform works on tailworks, surface by surface.
It's **measured, not claimed**: every ✅ and ⚠️ row names the tests that
prove it, and `make lint` fails if one of those tests doesn't exist. The
README's [Compatibility at a glance](../README.md#compatibility-at-a-glance)
table is the summary of this page; the two must agree.

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
table, in logical position, not at the end. `make lint` runs
`internal/compatdoc` to check all of this.

## Runtime

| Surface | Status | workerd provides | tailworks adds | Different from Cloudflare | Missing | Tests |
|---|---|---|---|---|---|---|
| <a id="fetch"></a>`fetch` handler and outbound `fetch()` | ⚠️ | the `fetch` handler, `Request`/`Response`, streams, outbound `fetch()` through the worker's `globalOutbound` | requests arrive over WireGuard with the caller's identity (`getCaller`, [below](#identity)); outbound `fetch()` leaves from the workload's node, or from the object's own node (facet mode), so tailnet policy decides what each can reach | outbound `fetch()` reaches **tailnet peers only**: a tailnet name that isn't in the netmap is refused with `peer_not_visible`, and an internet name with `internet_egress_unavailable` (a gateway on your tailnet is how to [call an internet API](recipes.md#call-an-internet-api)). There is no switch for Worker internet egress. Inbound is tailnet-only unless you add [public routes](#public-routes) | internet egress for Workers (only containers have an `egress.internet` allowlist, [containers](#containers)) | `hostd:TestHostd/HelloVerifiedCaller`, `hostd:TestHostd/EgressAttributedPerObject`, `hostd/gen:TestGeneratedWrapperProbe/WorkloadEgressIsWorkload`, `netd:TestChain/PeerNotVisible`, `netd:TestChain/InternetEgressUnavailable` |
| <a id="websockets"></a>WebSockets | ✅ | `WebSocketPair`, upgrade responses, the hibernation API | the upgrade is proxied through netd ingress and hostd to the object's own node | — | — | `e2e/sdk:TestCloudflareAgents/WebSocketsAndHibernation`, `netd:TestPublicUpgrade` |
| <a id="connect"></a>TCP sockets (`connect()`, `cloudflare:sockets`) | ❌ | `connect()` | — | — | tailworks' egress paths carry HTTP only, so `connect()` fails ("cannot connect to the specified address"). Raw TCP egress through netd isn't built; `ctx.tailnet.connect()` throws `plan_feature_missing` | `hostd/gen:TestRuntimeCompat/ConnectTCPRefused` |
| <a id="wasm"></a>WebAssembly modules | ✅ | `.wasm` modules (`import mod from "./x.wasm"`), `WebAssembly.Instance` | the platform loads `.wasm` modules from the bundle, in the Worker and in both object modes; `tw deploy`'s bundlers ship them as wrangler does (`<sha1>-<file>.wasm`, also `.wasm?module`) | — | — | `hostd/gen:TestRuntimeCompatWasm`, `internal/esbundle:TestBuildAppWranglerSettings`, `e2e/wrangler:TestDevWrangler/BuiltinEsbuild` |
| <a id="nodejs-compat"></a>`nodejs_compat` | ✅ | the `node:*` modules behind `compatibility_flags: ["nodejs_compat"]` | the bundler maps bare Node builtins (`buffer`, `crypto`, …) to `node:*` | — | only a sample is tested: `Buffer`, `node:crypto` (`createHash`, `randomUUID`), `AsyncLocalStorage`, `EventEmitter`; for real Node, see [Node apps](#node-runtime) | `hostd/gen:TestRuntimeCompat/NodejsCompat` |
| <a id="python"></a>Python Workers | ❌ | Python Workers (Pyodide) | — | — | tailworks' bundler is JavaScript-only. Python runs in a [container](#containers) | — |
| <a id="node-runtime"></a>Node.js apps (`runtime: "node"`) | ✅ | — (tw-only) | runs a plain Node.js process as a workload with its own tailnet node, egress through netd's proxy; runs the Vercel Workflow SDK. [Details](#node-process-tier-m9) | not a Cloudflare surface. One tailnet identity per process: no Durable Objects, no `tailnet.objects` | crons and public routes on the Node runtime | `e2e/node:TestNodeTier`, `e2e/node:TestNodeWorkflow`, `hostd:TestNodeSecrets` |

## Durable Objects

| Surface | Status | workerd provides | tailworks adds | Different from Cloudflare | Missing | Tests |
|---|---|---|---|---|---|---|
| <a id="do-identity"></a>One tailnet node per object | ✅ | — (tw-only) | every DO instance is its own tailnet node, with its own MagicDNS name, tags and grants; its egress is attributed to it; `tw kill` cuts it off | Cloudflare has no per-object network identity | — | `hostd:TestHostd/ObjectsGetOwnNodes`, `hostd:TestHostd/KillFailsFast`, `netd:TestChain/PerObjectEgressAttribution` |
| <a id="do-storage"></a>Storage: KV API and SQLite | ✅ | `ctx.storage` `get`/`put`/`list`/`delete`/`transaction`, `ctx.storage.sql` | storage on the host's local disk; it survives redeploys | an object lives on one host; there's no replication across hosts | point-in-time recovery (`getBookmarkForTime`, `onNextSessionRestoreBookmark`): not tested | `hostd/gen:TestRuntimeCompat/DOStorageKVAndSQL`, `hostd:TestHostd/BlueGreenRedeploy`, `e2e/sdk:TestCloudflareAgents/SetStateOnStateChangedSQL` |
| <a id="alarms"></a>Alarms | ✅ | `setAlarm`/`getAlarm`/`deleteAlarm`, `alarm()` | workerd facets can't set alarms, so tailworks delegates a facet's alarm to its Host DO (a shim); plain-DO objects use workerd's directly | — | — | `hostd/gen:TestGeneratedWrapperProbe/PlainDOAlarm`, `hostd:TestHostd/PlainDOAlarmWorkloadEgress`, `e2e/sdk:TestCloudflareAgents/Schedule` |
| <a id="hibernation"></a>Hibernation | ✅ | idle eviction; WebSocket hibernation (`acceptWebSocket`, attachments) | the object's tailnet node stays up while the object is evicted | — | — | `hostd:TestHostd/HibernationKeepsNode`, `e2e/sdk:TestCloudflareAgents/WebSocketsAndHibernation` |
| <a id="locks"></a>Locks / leader election (pattern) | ⚠️ | single-threaded named objects, storage, alarms | a fenced owner per workload (ADR-011/028): a host cut off from mgmt stops serving before another can take over; the `lock` template: leases with renewal, holders recorded by tailnet identity, fencing tokens that carry the lease epoch so they keep increasing across a host move, `tw audit` of holders. [docs/locks.md](locks.md) | DO storage doesn't move: after a takeover a lock starts free on the new host (safe because leases are capped below the takeover window). Failover is manual (`--force-takeover`), about 45 s after the host's last heartbeat | automatic failover; calling the lock workload over the tailnet from a workload on the same host (netd refuses sibling hairpins) | `hostd:TestLockTemplate`, `e2e/locks:TestLocksFailover` |
| <a id="do-rpc"></a>RPC (`stub.method()`) | ⚠️ | JS RPC to DO stubs | RPC from the Worker and from a sibling object is forwarded into the facet | — | bindings to another Worker's class (`durable_objects` `script_name`) are refused at deploy; call that workload over the tailnet instead | `hostd/gen:TestGeneratedWrapperProbe/RPCForwardedToFacet` |
| <a id="facets"></a>Facets from tenant code (`ctx.facets`, `subAgent()`) | ❌ | Durable Object facets | tailworks itself runs each facet-mode object as a facet (that's how each object gets its own egress) | — | inside a facet-mode object, the Dynamic Worker's `ctx.exports` has no tenant classes, so the Agents SDK's `subAgent()` fails. Plain-DO mode: not tested. Use `ctx.tailnet.spawn` (own node, narrower caps) | `e2e/sdk:TestCloudflareAgents/UnsupportedBindings`, `hostd:TestHostd/SpawnChildNode` |

## Bindings

| Surface | Status | workerd provides | tailworks adds | Different from Cloudflare | Missing | Tests |
|---|---|---|---|---|---|---|
| <a id="kv"></a>Workers KV | ⚠️ | the `kvNamespace` binding (the API, its argument checks and types), backed by a service you supply | each namespace is a platform-owned SQLite Durable Object on the workload's host, with every write journaled (sealed, lease-fenced) in mgmt first, so it survives restarts and moves to another host; works in facets and on the microVM tier; `tw kv namespace\|key …` and REST, like `wrangler kv`; `tw dev` keeps it between runs. [Details](#workers-kv-details) | a namespace belongs to one workload; read-your-writes on a host, other writers seen within 60 s (or `cacheTtl`); writes need mgmt reachable; values ≤ 2 MiB − 4 KiB | sharing a namespace between workloads, values over 2 MiB, `wrangler kv bulk`, `cacheStatus` (always `null`) | `hostd:TestWorkersKV`, `hostd:TestWorkersKVMove`, `e2e/vm:TestE2EVMKV`, `e2e/sdk:TestOAuthProviderKV`, `mgmt:TestKVAdmin`, `cmd/tw:TestCLIKV`, `cmd/tw:TestDevKVPersists` |
| <a id="r2"></a>R2 | ⚠️ | R2 bindings backed by a service you supply | `r2_buckets` bindings: `get`, `put`, `head`, `delete` (one key or many), `list` (`prefix`, `delimiter`, `cursor`, `startAfter`, `include`), `httpMetadata` and `customMetadata`, MD5/SHA checksums, ranged reads (`range`, a `Range` header), `onlyIf` on get and put, in the Worker and in both object modes. The bucket's index is a platform store (journaled to mgmt, fenced to the lease holder, survives a move); the bytes are blobs in the host's R2 backend: files on the host (`-r2-backend fs`, the default) or any S3-compatible service (`s3`). Only hostd holds the backend's credentials; bodies stream through. `tw r2 bucket ls\|create\|rm`, `tw r2 object ls`. [Details](r2.md) | a bucket belongs to one workload; each write is a round trip to mgmt; with the `fs` backend the bytes stay on the host, so after a move to another host old objects fail with `r2_blob_missing` (use `s3` for more than one host) | public buckets, custom domains, presigned URLs and the S3 API to a bucket; lifecycle rules, event notifications, storage classes, `ssecKey`, jurisdictions; `tw r2 object get\|put`; sharing a bucket between workloads; [bucket mounts in containers](#containers) | `hostd:TestR2Binding/fs/worker/PutGetHeadMetadata`, `hostd:TestR2Binding/s3/facet/PutGetHeadMetadata`, `hostd:TestR2Binding/s3/worker/Ranges`, `hostd:TestR2Binding/fs/facet/OnlyIf`, `hostd:TestR2Binding/s3/worker/ListDelete`, `hostd:TestR2Streaming`, `hostd:TestR2IndexSurvivesMove`, `e2e/placement:TestR2MoveBetweenHosts`, `e2e/vm:TestE2EVMR2`, `hostd/r2:TestBackends`, `hostd/r2:TestSigV4AWSExample`, `hostd/bundle:TestParseR2Buckets`, `cmd/tw:TestCLIR2` |
| <a id="r2-multipart"></a>R2 multipart upload | ⚠️ | `createMultipartUpload`, `resumeMultipartUpload`, `uploadPart`, `complete`, `abort` | each part is a blob; `complete` commits the object, listing its parts, in one journal append; a ranged read reads only the parts it overlaps. Part rules as R2: all parts but the last the same size, at least 5 MiB | a part's etag is its MD5 | uploads that are never completed or aborted are kept (R2 aborts them after 7 days) | `hostd:TestR2Binding/fs/worker/Multipart`, `hostd:TestR2Binding/s3/facet/Multipart`, `hostd:TestR2IndexSurvivesMove` |
| <a id="d1"></a>D1 (`d1_databases`, `prepare`/`bind`/`first`/`all`/`run`/`raw`, `batch`, `exec`) | ⚠️ | the D1 API (`cloudflare-internal:d1-api`), which speaks HTTP to a backend you supply | each database is a platform-owned SQLite Durable Object (ADR-038); writes go to mgmt's fenced, sealed journal before they apply, so a database survives a host restart, a host loss and a lease move (`--replace`); reads are local. The same API in facet-mode objects. Limits checked before SQLite runs a statement and before a result leaves (100 KB statement, 100 parameters, 2 MB value, 16 MiB result). [Details](d1.md) | named by `database_name` within one workload (`database_id` is ignored); a bound JS number is a REAL in a column without INTEGER affinity (as under `wrangler dev`, also Durable Object SQLite); `exec()` is atomic; one write (statement or batch) is at most 2 MiB encoded; every write waits for mgmt; when a host rebuilds a database from the journal, `random()`, `CURRENT_TIMESTAMP` and other non-deterministic SQL run again; `dump()` throws (use `tw d1 export`) | sharing a database between workloads; read replicas; Time Travel; journal compaction (the journal keeps every write, up to 1 GiB per database); a query-duration limit; the `d1_binding_jsrpc` flag's `query()` | `hostd:TestD1`, `hostd/gen:TestGenerateD1`, `hostd/bundle:TestParseD1`, `e2e/placement:TestD1SurvivesRestartAndMove` |
| <a id="d1-sessions"></a>D1 sessions (`withSession`, bookmarks) | ⚠️ | `withSession()`, `getBookmark()`, the `x-cf-d1-session-commit-token` header | a bookmark is the database's journal position: a read with a bookmark waits until the local copy has applied it; `first-primary` catches up with the journal first | there are no read replicas, so a session only matters across a workload's replicas. Without a session a read is served locally and catches up when its last sync is over 1 s old | — | `hostd:TestD1/Sessions` |
| <a id="d1-migrations"></a>D1 migrations (`migrations_dir`, `tw d1 migrations`) | ✅ | — | `tw d1 migrations create\|list\|apply`, wrangler's file layout and `d1_migrations` table (or `migrations_table`), each migration applied with its record as one atomic batch; `tw dev` applies pending migrations at start | `tw dev` applies them on its own (`wrangler dev` doesn't); `tw deploy` doesn't, as with wrangler | — | `internal/d1mig:TestMigrations`, `internal/d1mig:TestSplit`, `hostd:TestD1/Migrations`, `cmd/tw:TestCLID1`, `cmd/tw:TestDevD1` |
| <a id="d1-cli"></a>D1 operator commands (`tw d1`) | ⚠️ | — | `tw d1 create\|list\|delete\|execute --command\|--file\|export`, like `wrangler d1`, through mgmt's API (`POST …/d1/{name}/query`, `GET …/d1/{name}/export`); a write is audited (`d1_write`, never the SQL) | `execute`, `export` and `migrations` reach the database through the running workload that binds it, not on their own; there is no `--local`/`--remote` (dev databases live in `tw dev`) | `export` of more than 16 MiB; import of a file over 2 MiB in one go | `cmd/tw:TestCLID1`, `mgmt:TestD1Routes`, `hostd:TestD1/AdminQueryAndExport` |
| <a id="queues"></a>Queues (producer) | ✅ | the `queue` binding (`send`, `sendBatch`, `metrics`, `contentType` `json`/`text`/`bytes`/`v8`, `delaySeconds`) backed by a service you supply | the broker: queues live in mgmt's database (sealed per queue), so producer and consumer can be different workloads on different hosts; `delivery_delay` per binding; facets get a proxy to the same binding; `tw queues`. [Details](queues.md) | a queue is created on first use (also `tw queues create`); `send()` errors read `Error: [<code>] … (<status>)` with tailworks' codes | per-queue settings (`message_retention_period`, a queue-level `delivery_delay`) | `hostd:TestQueues/ContentTypes`, `hostd:TestQueues/DelaySeconds`, `hostd:TestQueues/FromFacet`, `hostd:TestQueues/MetricsAndLimits`, `mgmt:TestQueuesSendPullAck`, `cmd/tw:TestCLIQueues` |
| <a id="workflows"></a>Workflows (`workflows`, `WorkflowEntrypoint`) | ✅ | the built-in engine (`/workerd/workflows.js`, `--experimental`): each instance a SQLite-backed Durable Object; `step.do` with retries, timeouts and delays, `step.sleep`/`sleepUntil`, `step.waitForEvent`, instance `create`/`createBatch`/`get`/`status`/`pause`/`resume`/`terminate`/`restart`/`sendEvent` | instances live in the workload's Durable Object storage on its lease holder (fenced with it; in the microVM's encrypted volume), with no tailnet node of their own; a Workflow's egress is the workload node's; the binding works in the Worker and in both object modes. [Approvals](#approvals) by tailnet identity | an event of type `approval` or `approval-*` can only come from tailworks' [approval surface](approvals.md), never from app code (`approval_requires_identity`), and no event the app sends may carry an `approver` (it is removed) | `script_name` (another Worker's Workflow) and `schedules` are refused at deploy; `subscribe()` and `deleteBatch` not tested | `hostd/gen:TestGeneratedWorkflows`, `hostd/gen:TestParseWorkflows`, `e2e/sdk:TestWorkflowApprovals/RetryFromFailedStep`, `e2e/sdk:TestWorkflowApprovals/SurvivesHostdRestart` |
| <a id="workers-ai"></a>Workers AI (`env.AI`) | ❌ | — | — | — | no binding. Point an AI SDK provider at an LLM gateway on your tailnet (the `ai-agent` template) | `e2e/sdk:TestCloudflareAgents/UnsupportedBindings` |
| <a id="vectorize"></a>Vectorize | ❌ | — | — | — | no binding | — |
| <a id="hyperdrive"></a>Hyperdrive | ❌ | — | — | — | no binding, and no [`connect()`](#connect) for a database driver. A [Node app](#node-runtime) can reach a database over the tailnet | — |
| <a id="browser"></a>Browser Rendering (`env.BROWSER`) | ❌ | — | — | — | no binding | `e2e/sdk:TestCloudflareAgents/UnsupportedBindings` |
| <a id="email"></a>Email (`cloudflare:email`, `send_email`, `email()`) | ❌ | `EmailMessage`, the `email()` handler | the bundler stubs `cloudflare:email`, so constructing an `EmailMessage` throws "not supported on tw" | — | sending and receiving. Funnel has no port 25: receive through a provider's webhook to a [public route](#public-routes) | `e2e/sdk:TestCloudflareAgents/UnsupportedBindings` |
| <a id="service-bindings"></a>Service bindings (`services`) | ❌ | service bindings, including RPC (`WorkerEntrypoint`) | — | — | `wrangler.jsonc` `services` is not read. Call the other workload by its MagicDNS name over the tailnet; it sees your workload as the caller | — |
| <a id="secrets"></a>Secrets | ⚠️ | text bindings | `tw secret put\|ls\|rm` and `.dev.vars`, like `wrangler secret` and `wrangler dev`; `process.env` on [Node apps](#node-runtime). [Details](#secrets-details) | a secret overrides a var of the same name; a change takes effect on the next `tw deploy`; the microVM tier refuses workloads with secrets | `wrangler secret bulk`, secret versions, the Secrets Store binding | `hostd:TestWorkerSecrets`, `hostd:TestNodeSecrets`, `hostd/gen:TestGenerateSecrets` |
| <a id="vars"></a>Environment variables (`vars`) | ✅ | text and JSON bindings | the same `env` in the Worker and in both object modes; platform bindings are never visible to tenant code | — | — | `hostd/gen:TestGeneratedWrapperProbe/FacetEnvAndPrivacy` |
| <a id="assets"></a>Static assets (`assets`) | ❌ | — | — | — | `wrangler.jsonc` `assets` is not read; serve files from your `fetch` handler | — |

## Triggers

| Surface | Status | workerd provides | tailworks adds | Different from Cloudflare | Missing | Tests |
|---|---|---|---|---|---|---|
| <a id="cron"></a>Cron triggers | ✅ | the `scheduled()` handler | hostd runs `triggers.crons` on the host serving the active deployment (Cloudflare's syntax, UTC, 1 = Sunday). A run has no caller; its egress leaves from the workload's node. [Details](cron-triggers.md) | no backfill of missed minutes; an overlapping run is skipped, not queued; no retries | crons on the Node runtime | `hostd:TestCronTriggers`, `hostd/gen:TestGeneratedCron`, `hostd/bundle:TestParseCrons` |
| <a id="queue-consumers"></a>Queue consumers (`queue()`) | ⚠️ | the `queue()` handler and `MessageBatch` (`ack`, `retry`, `ackAll`, `retryAll`) | hostd on the host holding the consumer's lease claims batches from mgmt (`max_batch_size`, `max_batch_timeout`), runs `queue()` and settles them; `max_retries`, `retry_delay`, `dead_letter_queue`; at-least-once across a workerd crash, a hostd restart and a lease move. [Details](queues.md#delivery) | several workloads may consume one queue (competing); `max_concurrency` is a fixed number of loops, not autoscaling | consumers on the Node runtime | `hostd:TestQueues/BatchBySize`, `hostd:TestQueues/BatchByTimeout`, `hostd:TestQueues/RetryWithDelay`, `hostd:TestQueues/MaxRetriesDeadLetter`, `hostd:TestQueues/ThrowRetriesBatch`, `hostd:TestQueues/ConsumerCrash`, `e2e/queues:TestQueuesMultiHost`, `cmd/tw:TestDevQueues` |
| <a id="queue-pull"></a>Queue pull consumers (HTTP pull) | ⚠️ | — | `POST …/queues/{q}/messages/pull` and `…/ack` on mgmt's API: lease IDs, visibility timeout, per-message retry delays, `maxRetries` and a dead-letter queue | tailworks' API, not Cloudflare's REST API; the caller needs tailworks' admin capability (tailnet identity, no token) | a per-queue grant for non-admin pull clients | `mgmt:TestQueuesDelayRetriesDLQ`, `mgmt:TestQueuesLongPoll`, `mgmt:TestQueuesFence` |

## Platform

| Surface | Status | workerd provides | tailworks adds | Different from Cloudflare | Missing | Tests |
|---|---|---|---|---|---|---|
| <a id="identity"></a>Caller identity and auth | 🚫 | — | `getCaller(req)`: the caller's tailnet node, user, tags and grant capabilities, from WireGuard; client copies of identity headers are stripped | by design there are no bearer tokens or API keys between workloads, and no Cloudflare Access JWTs: who's calling is the WireGuard peer, and what it may do is your tailnet policy (RFC) | on-behalf-of assertions (`actingFor`, RFC §4.6) | `hostd:TestHostd/HelloVerifiedCaller`, `hostd:TestHostd/ForgedHeadersStripped`, `netd:TestChain/ForgedIngressHeadersStripped` |
| <a id="approvals"></a>Workflow approvals by tailnet identity | ✅ | — (tw-only) | a Workflow that waits for an `approval` event (`waitForApproval`) is listed (`tw approvals`, the admin UI, `GET /_tw/approvals` on the workload's node) and decided only by a tailnet identity holding `github.com/rosskukulinski/tailworks/cap/approve` (workload, Workflow, step, decision and numeric-param limits), checked by tailworks before delivery; the Workflow sees `approver` (user, node, tags) from WireGuard; every request, decision, refusal and timeout is audited. [Details](approvals.md) | Cloudflare has no identity-bound approvals: there, any code holding the binding sends the event | email or chat notifications (the app sends its own); escalation beyond reject-on-timeout | `e2e/sdk:TestWorkflowApprovals`, `e2e/sdk:TestRefundApprovalTemplate`, `hostd:TestServeApprovalsRefusals`, `hostd:TestAuthorizeApproval`, `hostd:TestApprovalRegistry`, `mgmt:TestApprovals`, `cmd/tw:TestApproveCLI` |
| <a id="placement"></a>Placement (`placement`) | ⚠️ | — | `"mode": "smart"` places a workload at deploy on the host with the best measured tailnet path to what it connects to. [Details](#workers-platform-config) | a different signal (path class and RTT, not request durations). By design, tailworks never moves a running workload on its own; `tw deploy --replace` re-places it. `placement.region` is ignored with a warning | per-object placement (`locationHint`) | `mgmt:TestScoreHosts`, `mgmt:TestAPIPlacement`, `mgmt:TestResolvePlacement`, `e2e/placement:TestSmartPlacementHermetic` |
| <a id="public-routes"></a>Public routes | ⚠️ | — | `public.routes` in `tailworks.jsonc` serves chosen paths over Tailscale Funnel; `getRequester(req)` returns `{kind: "public", ip}`; public requests can't create objects unless the class allows it. [Details](#public-routes-funnel-adr-029) | tailnet-only by default (Cloudflare Workers are public by default); `*.ts.net` names only | custom domains; `request.cf` geo fields | `hostd:TestPublicIngress`, `netd:TestPublicRoutes`, `netd:TestPublicHeadersAndBlob`, `hostd/gen:TestGeneratedWrapperPublic` |
| <a id="containers"></a>Containers (`ctx.container`, Sandbox SDK) | ⚠️ | — | each container is its own microVM attached to one Durable Object; egress through the object's identity, `enableInternet` limited to `egress.internet`. [Details](#containers-ctxcontainer-and-the-cloudflare-sandbox-sdk-adr-032) | needs a microVM-capable host (Linux with KVM, or macOS); amd64 not verified | `getTcpPort().connect()`, snapshots, `exposePort` / preview URLs, backups, bucket mounts | `e2e/container:TestE2EContainer`, `e2e/container:TestE2EContainerRegistry`, `hostd:TestContainerEgress` |
| <a id="logs"></a>Logs and tail | ⚠️ | `console.*` output; tail events (`TraceItem`) | `tw logs <workload>` shows one structured record per request: the verified tailnet caller, status, duration, outcome, `console.*` lines with their level, and exceptions (workerd's tail joined with tailworks' ingress), in the Worker and both object modes; `--json`, `--errors`, `--status`, `--object`, `--caller`, `--trace`; follows; works from a `tw link`ed machine (replicas prefixed by host); `--console` for the raw output. [Details](#logs-metrics-and-traces-adr-observability) | records live on the workload's host (rotating, rate-limited per workload) and are exported with OTLP, not queried in a dashboard; the filters differ from `wrangler tail`'s | Tail Workers for your code (`tail()`, `tail_consumers`), Workers Logs queries, `--search`/`--header`/sampling | `hostd:TestTelemetry/RecordsJoinIngressAndTail`, `hostd:TestTelemetry/ExceptionAndNoQueryString`, `hostd:TestTelemetry/LogsAPI`, `hostd:TestRecordRateLimit`, `hostd:TestLogsHandlerTailAndRefusals`, `mgmt:TestWorkloadLogs`, `cmd/tw:TestRemoteLogs`, `e2e/observability:TestObservabilityHermetic` |
| <a id="metrics"></a>Metrics (Workers metrics, Analytics) | ⚠️ | — | `GET /metrics` (Prometheus) on each host's and mgmt's own tailnet node, port 9464 (a grant in your tailnet policy decides who scrapes), and each workload's metrics over OTLP: requests, errors and latency (`http.server.request.duration`), egress per object, invocations by outcome, DO objects and wake-ups, host capacity and usage, mint stages. OpenTelemetry semantic-convention names. [Details](observability.md#metrics-get-metrics) | no dashboard or GraphQL Analytics API: bring Prometheus or a collector | CPU time (workerd reports 0); the Workers Analytics Engine binding | `hostd:TestTelemetry/PrometheusScrape`, `internal/obs:TestPrometheusExposition`, `internal/obs:TestSeriesLimitOverflow`, `e2e/observability:TestObservabilityHermetic` |
| <a id="traces"></a>Tracing (`traceparent`, Workers Traces) | ⚠️ | — | W3C `traceparent` across caller → Worker → Durable Object (stub `fetch()`) → egress → the next tailnet hop, a span per hop carrying the object and the verified caller, exported over OTLP. [Details](observability.md#traces) | trace context never feeds authorization or audit: the audit trace ID stays tailworks' own per hop; a public (Funnel) request starts a new trace | spans for DO RPC (`stub.method()`), alarms, cron runs and storage operations | `hostd:TestTelemetry/TraceparentAcrossDOAndEgress`, `hostd:TestTelemetry/OTLPExportFromTheWorkloadNode`, `hostd:TestIngressTraceContext`, `js/packages/tailnet/test/trace.test.js:a DO stub fetch gets a child span, reported when the handler ends`, `e2e/observability:TestObservabilityHermetic` |
| <a id="otlp-export"></a>Export to OpenTelemetry (Workers Observability destinations, Logpush) | ⚠️ | — | `tw telemetry set --otlp <url> [--header NAME]`: each workload's request records, metrics and spans over OTLP/HTTP (JSON); collector credentials kept in mgmt's custody like secrets. [Details](observability.md#export-to-a-collector-tw-telemetry-set) | by design the collector is a tailnet peer, dialed from each workload's own node, so your tailnet policy decides who may export where; no public destinations | per-workload config and sampling; Logpush to object storage | `hostd:TestTelemetry/OTLPExportFromTheWorkloadNode`, `mgmt:TestTelemetryRelease`, `mgmt:TestTelemetryRoles`, `cmd/tw:TestCLITelemetry`, `e2e/observability:TestObservabilityHermetic` |
| <a id="wrangler-config"></a>Wrangler config (`wrangler.toml`, `wrangler.json`, `wrangler.jsonc`) | ⚠️ | — | `tw deploy` and `tw dev` read whichever file wrangler would (json, then jsonc, then toml) and use `name`, `main`, `compatibility_date`, `compatibility_flags`, `vars`, `durable_objects`, `kv_namespaces`, `r2_buckets`, `migrations`, `triggers.crons`, `queues`, `placement`, `containers`, `workflows` and the build settings; a project without a `tailworks.jsonc` gets defaults ([wrangler.md](wrangler.md)) | only the app directory is searched (wrangler also searches parents) | the keys tailworks can't use (`routes`, `workers_dev`, binding sections it has no binding for, `assets`, …) are ignored with one warning line each. [Details](#wrangler-config-details) | `cmd/tw:TestFindWranglerConfigPrecedence`, `cmd/tw:TestReadWranglerTOML`, `cmd/tw:TestWranglerWarnings`, `cmd/tw:TestLoadAppWranglerTOMLOnly`, `cmd/tw:TestCLIDeployWranglerTOML`, `hostd/bundle:TestParseCrons`, `hostd/bundle:TestParseQueues`, `hostd/bundle:TestParseContainers`, `hostd/gen:TestParseWorkflows`, `hostd/bundle:TestParseKVNamespaces`, `mgmt:TestResolvePlacement` |
| <a id="wrangler-env"></a>Environments (`env.<name>`, `--env`) | ✅ | — | `tw deploy --env <name>` / `tw dev --env <name>` resolve `env.<name>` with wrangler's inheritance (`vars`, `define` and bindings are not inherited, warned per key as wrangler does) | each environment is its own workload, `<name>-<env>` unless the env sets `name`; `tailworks.jsonc` has no environments | — | `cmd/tw:TestApplyWranglerEnv`, `cmd/tw:TestCLIDeployWranglerTOML` |
| <a id="wrangler-bundling"></a>Bundling (`define`, `alias`, `rules`, custom builds) | ✅ | — | the built-in esbuild honours `define`, `alias`, `rules` (Text, Data, CompiledWasm, with wrangler's defaults), `minify`, `keep_names`, `jsx_*`, `tsconfig`; `--wrangler` bundles with `wrangler deploy --dry-run --outdir`, and `--from-wrangler-out <dir>` packs such a directory (needed for `build`, `no_bundle`, `find_additional_modules`) | — | CommonJS and Python modules | `internal/esbundle:TestBuildAppWranglerSettings`, `internal/esbundle:TestRuleGlobs`, `cmd/tw:TestBuildWranglerTOMLApp`, `e2e/wrangler:TestDevWrangler/FromWranglerOut`, `e2e/wrangler:TestDevWrangler/WranglerCLI` |
| <a id="wrangler-api"></a>`wrangler deploy`, `wrangler secret`, `wrangler tail` against tailworks | 🚫 | — | `tw deploy`, `tw secret`, `tw logs -f` take the same project | tailworks serves no Cloudflare-compatible API: it would have to track about 50 wrangler endpoints across frequent wrangler releases and bring bearer tokens back, and preflight, placement and image pinning don't fit wrangler's flow ([why](wrangler.md#wrangler-commands-that-call-cloudflare)) | — | — |

## SDKs

| Surface | Status | workerd provides | tailworks adds | Different from Cloudflare | Missing | Tests |
|---|---|---|---|---|---|---|
| <a id="agents-sdk"></a>Cloudflare Agents SDK (`agents`) | ⚠️ | — | runs unmodified: routing, state, SQL, schedules, WebSockets and hibernation, MCP client and server, in both object modes. `getCaller` in `onRequest` and `onConnect`. `AgentWorkflow` and `waitForApproval`, with the approver's verified identity. [Details](#cloudflare-agents-sdk-agents-0330) | an agent can't call an MCP server on the same host over the tailnet (`forbidden_destination`); `approveWorkflow()`/`rejectWorkflow()` from app code are refused by design ([approvals](#approvals)) | `subAgent()`, `env.AI`, email, browser, codemode; `McpAgent`, `@callable` and `AIChatAgent` not tested | `e2e/sdk:TestCloudflareAgents/RouteAgentRequest`, `e2e/sdk:TestCloudflareAgents/Schedule`, `e2e/sdk:TestCloudflareAgents/AgentAsMCPClient`, `e2e/sdk:TestAgentTemplate`, `e2e/sdk:TestWorkflowApprovals` |
| <a id="mcp"></a>MCP servers (`createMcpHandler`, `mcp-handler`) | ✅ | — | `@tailworks/mcp` `withTailnet`: a per-request caller, and tools allowed or denied by tailnet grants (`cap_missing`) | — | stdio transport | `e2e/sdk:TestCloudflareAgents/CreateMcpHandlerWithTailnet`, `e2e/sdk:TestVercelAI/McpHandlerWithTailnet` |
| <a id="vercel-ai"></a>Vercel AI SDK (`ai`) | ⚠️ | — | `ToolLoopAgent` and UI message streams run in the Worker and in objects (one agent, one node). [Details](#vercel-ai-sdk-ai-70165-and-mcp-handler-231) | `@ai-sdk/mcp` with the default `fetch` throws in workerd; use `@tailworks/mcp` `mcpFetch` | `WorkflowAgent` in the Worker (it runs on [Node apps](#node-runtime)), on-behalf-of assertions | `e2e/sdk:TestVercelAI/ToolLoopAgent`, `e2e/sdk:TestVercelAI/EgressRefusalIsTyped`, `e2e/node:TestNodeWorkflow` |

## Tooling

| Surface | Status | workerd provides | tailworks adds | Different from Cloudflare | Missing | Tests |
|---|---|---|---|---|---|---|
| <a id="cli-json"></a>CLI `--json` output | ✅ | — (tw-only) | every command's result is one JSON document matching [`schema/cli.schema.json`](../schema/cli.schema.json); every error is `{code, message, hint, doc_url}` with a code from [errors.md](errors.md); `tw deploy` streams progress events (stderr), `tw dev` and `tw logs` stream NDJSON (stdout). [Details](cli-json.md) | not a Cloudflare surface (wrangler's `--json` covers some commands) | `status`'s `workloads[]`, `image push` and `host vmcheck --container` use Go field names | `cmd/tw:TestCLIJSONContract`, `cmd/tw:TestAgentEval`, `cmd/tw:TestDevJSONEvents`, `cmd/tw:TestRemoteLogs` |
| <a id="agent-skills"></a>Coding-agent skills (`tw agent`) | ✅ | — (tw-only) | ten skills built into the CLI (scaffold, dev, deploy, debug, policy, secrets, cron, public routes, teardown); `tw agent setup` installs them for Claude Code, Codex, Cursor, Gemini CLI and OpenCode, per user or per project, after showing what it writes; `tw agent remove` undoes it. [Details](agents.md) | not a Cloudflare surface | per-user Cursor rules (Cursor keeps them in its settings) | `cmd/tw:TestSkillCommandsExist`, `cmd/tw:TestSkillsWellFormed`, `cmd/tw:TestAgentEval`, `cmd/tw:TestAgentSetupSnapshot`, `agent:TestPlanBlock`, `agent:TestPlanStaleAndForeign` |
| <a id="vitest-plugin"></a>Testing: `@cloudflare/vitest-plugin` | ⚠️ | — (Cloudflare's test runner; it brings its own workerd via miniflare) | `@tailworks/tailnet/testing`: `withCaller` / `withPublic` (a caller in `request.cf`, which survives service bindings and Durable Object stubs) and a fake `ctx.tailnet` (spawn, kill, audit, self, fetch); black-box tests through `tw dev --caller` and `@tailworks/tailnet/testing/dev`; every template ships an `npm test`. [Details](testing.md) | plain Worker, Durable Object (storage, alarms), Agents SDK, Workflows and MCP-handler logic run as on Cloudflare; `getCaller` and `ctx.tailnet` need the helper (nothing delivers a caller in vitest-plugin) | approvals and per-object egress can only be tested against `tw dev`; `tw dev` can't choose a caller's login and has no policy evaluator yet; no `withCaller` for the Node tier | `e2e/vitest/test/lock.test.ts:withCaller reaches getCaller through the Worker and the Durable Object stub`, `e2e/vitest/test/agent.test.ts:Agents SDK state and SQL: one agent per caller`, `e2e/vitest/test/mcp.test.ts:a granted tool runs; a tool outside the grant is cap_missing; a limit is enforced`, `e2e/vitest/test/refund-approval.test.ts:a refund request starts a Workflow that waits for approval`, `js/packages/tailnet/test/testing.test.js:withCaller refuses to run outside test mode`, `cmd/tw:TestDevCallers`, `cmd/tw:TestTemplateTests` |
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
| workerd | 1.20261115.1 | platform runtime; the built-in Workflows engine (`/workerd/workflows.js`, workerd#8331) is in 1.20261114.1 and later |
| `agents` (Cloudflare Agents SDK) | 0.33.0 | npm `latest` on 2026-11-08; runs on the pinned workerd unmodified |
| `@modelcontextprotocol/server` / `client` | 2.4.0 | MCP SDK v2 (agents' peers; `createMcpHandler`, `addMcpServer`) |
| `@modelcontextprotocol/sdk` | 1.32.2 | v1, only for agents' deprecated `McpAgent` (not used) |
| `ai` (Vercel AI SDK) | 7.0.165 | `ToolLoopAgent`, `ai/test` `MockLanguageModelV4` |
| `@ai-sdk/provider` / `provider-utils` | 4.0.37 / 5.0.76 | |
| `@ai-sdk/mcp` | 2.0.93 | HTTP transport only |
| `mcp-handler` (Vercel) | 2.3.1 | MCP SDK v2, 2026-07-28 spec |
| `zod` | 4.6.7 | |
| `@cloudflare/workers-oauth-provider` | 1.2.4 | `OAUTH_KV` on tailworks' KV binding (`TestOAuthProviderKV`) |

Egress modes (ADR-019 #5, `docs/egress-modes.md`): **facet** (default; the
object's `fetch()` leaves from its own node) and **plain-DO**
(`"egress": "workload"`; egress leaves as the workload node). Every Agent
check below runs in both.

## Cloudflare Agents SDK (`agents` 0.33.0)

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
| `addMcpServer` to an MCP server on the **same host** | ❌ | ❌ | `SameHostMCPRefused` | netd refuses sibling hairpins (`forbidden_destination`, RFC §4.5); the SDK surfaces it as "HTTP 403" (no fetch hook to type it). Same-host object→object calls over the tailnet are not built |
| `subAgent()` / sub-agent facets | ❌ | ? | `UnsupportedBindings` | inside a facet the Dynamic Worker's `ctx.exports` has no tenant classes ("Sub-agent class … not found in worker exports"). Use `ctx.tailnet.spawn` (own node, narrower caps) |
| `env.AI` (Workers AI) | ❌ | ❌ | `UnsupportedBindings` | no binding. Use an AI SDK provider pointed at a tailnet LLM gateway (`ai-agent` template, `src/model.ts`) |
| Workers KV binding (incl. `OAUTH_KV` for workers-oauth-provider) | ✅ | ✅ | `TestOAuthProviderKV` | `@cloudflare/workers-oauth-provider` 1.2.4 unmodified: registration, PKCE code flow, refresh, the protected API, `listClients`/`listUserGrants`, replay revocation. See [Workers KV](#workers-kv-details). For tailnet callers `getCaller` is still simpler than OAuth |
| Workflows: `AgentWorkflow`, `runWorkflow`, `getWorkflowStatus`, `step.reportComplete` | ✅ | ✅ | `TestWorkflowApprovals`, `TestRefundApprovalTemplate` | on workerd's built-in engine. A failed step retries from itself (completed steps return their stored results); the instance survives a hostd restart. Tailworks keeps `AgentWorkflow` from reporting the engine's end of a long wait (`WorkflowAbortedError`) to the Agent as an error |
| `waitForApproval` | ✅ | ✅ | `TestWorkflowApprovals/ApproveWithIdentity`, `TestWorkflowApprovals/Reject` | resolved only by tailworks ([approvals](#approvals)): the returned metadata carries `approver` (user, node, nodeId, tags, via) and `comment`; a forged `approver` in the request is replaced; a rejection throws `WorkflowRejectedError` with the comment as reason; a timeout rejects |
| `approveWorkflow` / `rejectWorkflow` from app code | 🚫 | 🚫 | `TestWorkflowApprovals/NoGrantRefused` | by design: an approval is a decision by a tailnet identity, so app code may not send one (`approval_requires_identity`); approve with `tw approve`, the admin UI or `POST /_tw/approvals/<workflow>/<instance>` |
| Email (`cloudflare:email`, `routeAgentEmail`) | ❌ | ❌ | `UnsupportedBindings` | the bundler stubs `cloudflare:email`; constructing `EmailMessage` throws "not supported on tw" |
| Browser Rendering (`env.BROWSER`, `agents/browser`) | ❌ | ❌ | `UnsupportedBindings` | |
| Codemode / dynamic agents (`env.LOADER` for tenants) | ❌ | ❌ | `UnsupportedBindings` | Worker Loader is platform-only |
| `harness/container/*` | not tested | not tested | — | was ❌ (needed a container). Tailworks now implements `ctx.container` (ADR-032, below), the primitive it builds on, but no `harness/container` code has been run on tailworks |
| `McpAgent` | not tested | | — | deprecated upstream; use `createMcpHandler` (ADR-022) |
| `@callable` RPC, `useAgent`, `AIChatAgent` | not tested | | — | same DO/WebSocket primitives as above (assumption) |

## Secrets: details

`tw secret put|ls|rm` and `.dev.vars` work like `wrangler secret` and
`wrangler dev`. Your code reads a secret as `env.NAME`, a text binding,
in the Worker and in Durable Objects. For the `node` runtime it is
`process.env.NAME` (loaded by a preload, not passed in the process
environment). See [secrets.md](secrets.md).

There are three differences from Cloudflare:

- A secret **overrides** a var of the same name; Cloudflare doesn't allow
  both.
- A change takes effect on the next `tw deploy`.
- The microVM tier refuses workloads that have secrets
  (`secrets_unsupported_tier`).

`wrangler secret bulk`, secret versions and the Secrets Store binding are
not supported.

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
- **Availability.** A write needs mgmt; reads keep working from the local
  copy when mgmt is down.
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
| Automatic tracing (Workers Traces): `traceparent` across requests | ⚠ partial | `TestTelemetry/TraceparentAcrossDOAndEgress`, `e2e/observability` | spans for tailworks' ingress (SERVER), DO stub `fetch()` (CLIENT, from the runtime) and egress (CLIENT); one trace across caller → Worker → DO → egress → the next hop. Not traced: DO RPC (`stub.method()`), alarms, cron runs, storage operations |
| Workers metrics / Analytics (requests, errors, duration) | ✅ Prometheus + OTLP | `TestTelemetry/PrometheusScrape`, `e2e/observability` | `GET /metrics` on port 9464 of each host's and mgmt's own tailnet node (a policy grant decides who scrapes), OpenTelemetry semantic-convention names; per tenant via OTLP |
| CPU time (`cpuTime` in tail events, the CPU time metric) | ❌ | `TestTelemetry` | workerd reports `cpuTime: 0` and `wallTime: 0`; tailworks reports wall time from its ingress |
| Workers Analytics Engine binding | ❌ | — | not a binding on tailworks |

## Vercel AI SDK (`ai` 7.0.165) and `mcp-handler` 2.3.1

| Feature | Status | Test | Notes |
|---|---|---|---|
| `ToolLoopAgent.stream()` in a Durable Object (facet) | ✅ | `ToolLoopAgent/*` | `MockLanguageModelV4` (scripted: tool calls, then text). One agent = one node: the MCP server saw `…-aiagent-<name>` as the caller |
| `ToolLoopAgent.stream()` in the Worker | ✅ | `ToolLoopAgent/*` | caller = the workload node |
| `toUIMessageStreamResponse()` (SSE, what `useChat` reads) | ✅ | `ToolLoopAgent/*` | 36–38 events; first event 50–400 ms after the request on this Mac |
| `@ai-sdk/mcp` `createMCPClient` (HTTP transport) → tailworks MCP server over the tailnet | ✅ with `mcpFetch` | `ToolLoopAgent/*` | `tools()` from both `mcp-handler` and CF `createMcpHandler`; allowed tool output and denied tool (`cap_missing`) reach the model |
| `@ai-sdk/mcp` with the default `fetch` | ❌ | — | its default `redirect: "error"` throws in workerd ("Invalid redirect value"). `@tailworks/mcp` `mcpFetch(ctx)` emulates it (manual + check) |
| `@ai-sdk/mcp` stdio transport | ❌ | — | `child_process` (Node process tier) |
| `mcp-handler` `createMcpHandler` + `withTailnet` | ✅ | `McpHandlerWithTailnet` | Web-standard handler mounted at `/mcp`; per-request caller, allow/deny, `whoami` |
| Typed egress refusals through the MCP client | ✅ | `EgressRefusalIsTyped` | `mcpFetch` throws `TailnetError`; the template renders it (`403 peer_not_visible`) instead of a 500 |
| `traceparent` propagation | ✅ | `TraceparentPropagated`, `TestTelemetry/TraceparentAcrossDOAndEgress` | `mcpFetch` sends `00-<caller traceId>-<span>-01`, and tailworks itself propagates the W3C trace across ingress, DO calls and egress (ADR-045). The **audit** trace ID (`getCaller().traceId`) is still each hop's own: the header is never trusted for audit or identity |
| On-behalf-of (`actingFor`) assertions on MCP calls | ❌ | — | RFC §4.6 not implemented: the server sees the agent's node, not the human it acts for |
| `WorkflowAgent` / Workflow SDK | ❌ | — | `node:vm` (ADR-022: Node process tier, branch m9) |
| `HarnessAgent`, Vercel Sandbox, hosted Vercel services | ❌ | — | client-only or process tier (Cloudflare's Sandbox SDK: see below) |

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
| `enableInternet` | ✅ allowlist only (`tailnet.egress.internet`), host's own network | ⚠ | ✅ | ❌ | `Egress/AllowlistedInternet` |
| `getTcpPort().connect()`, WebSocket to a container port | ❌ deferred | ❌ | ❌ | ❌ | |
| `snapshotContainer`, `containerSnapshot`, directory snapshots | ❌ v1.1 / deferred | ❌ | ❌ | ❌ | |
| `@cloudflare/containers` `Container` class (0.3.x) | ✅ (what Sandbox 0.x runs on) | ⚠ | ✅ | ❌ | `A_SandboxSDK0x_minimal` |
| Sandbox SDK 0.x (`@cloudflare/sandbox` 0.12.14) `examples/minimal`: `getSandbox`, `exec`, `writeFile`/`readFile` | ✅ unmodified, arm64 rebuild of `cloudflare/sandbox:0.12.14` via the alias map | ⚠ stock image, not verified | ✅ stock amd64 image under Rosetta (`-vm-rosetta`), or the arm64 rebuild | ❌ | `A_SandboxSDK0x_minimal` |
| Sandbox SDK 1.0 (`@cloudflare/sandbox` 1.0.x): raw `ctx.container.start` + `exec().output()` + `Files` via `sandbox-shim` | ✅ unmodified | ⚠ | ✅ | ❌ | `B_SandboxSDK10_minimal`, `B_raw_ctx_container_python_Files` |
| Sandbox SDK processes, sessions, code interpreter, streaming | not tested | not tested | not tested | ❌ | they use the same `exec` / port paths (assumption) |
| Sandbox SDK `exposePort` / preview URLs (`proxyToSandbox`) | ❌ `not_supported` / `501` (ADR-029) | ❌ | ❌ | ❌ | `TestContainerEgress` (hostd: facet and plain-DO) |
| Sandbox SDK backups, bucket mounts (R2) | ❌ | ❌ | ❌ | ❌ | |
| Images from the app's own Dockerfile via `tw deploy` (built on the developer machine, pinned by digest, pulled by the host) | ✅ | ⚠ (pinning by arch is unit-tested) | not run end to end (host pull uses the same code) | ❌ | `TestE2EContainerRegistry` |

amd64 is "⚠ not verified": the code and the amd64 guest assets exist, but
the only x86 test host (a nested KVM guest) loses interrupts in its L2 VMs, so
no amd64 microVM has booted to ready there (STATUS.md, ADR-032 section).

## Workers platform config

| Cloudflare | tailworks | Test | Notes |
|---|---|---|---|
| Smart Placement `placement: {"mode": "smart"}` | ✅ different signal | `TestScoreHosts`, `TestAPIPlacement`, `e2e/placement`, multimgmt S12 | Cloudflare moves a Worker toward its backend by analysing request durations per location (up to 15 min after deploy, `SUCCESS` / `INSUFFICIENT_INVOCATIONS`). Tailworks places a workload **at deploy** on the host with the best **measured tailnet path** to what it connects to (`tailnet.connects`, or `placement.targets`): direct WireGuard > peer relay > DERP > unmeasured, then RTT; free capacity breaks ties. Smart is the default when `connects` is set; `"mode": "off"` turns it off. Read from `tailworks.jsonc`, or from `wrangler.jsonc` `placement` if `tailworks.jsonc` has none. ADR-033 (Proposed) |
| `placement.host` / `placement.hostname` (probe targets) | ✅ as one target | `TestResolvePlacement` | the name (port dropped) is resolved in the tenant's netmap (MagicDNS name, host name or IP); Cloudflare probes it from public IPs with TCP/HTTP, tailworks with disco pings from the tenant's own nodes |
| `placement.region` (`aws:us-east-1`) | ⚠ ignored, warned | `TestResolvePlacement` | there are no cloud regions; the deploy's `placement.summary` says so |
| Placement status (`cf-placement` header, `placement_status`) | ⚠ different | `TestAPIPlacement` | `tw deploy` prints why (`placed on h2: tag:db direct 1.8 ms (h1: relayed 24 ms, h3: unmeasured)`); `GET /v1/deployments/{id}` `placement` and `tw ls` show it; the deploy audit record carries it. No per-request header |
| Re-placement while running | ❌ by design | `TestAPIPlacement`, `e2e/placement` | Cloudflare re-routes continuously. Tailworks never moves a running workload on its own (the lease holder is sticky); `tw deploy --replace` re-places it explicitly (hysteresis: a better path class, or ≥ 2 ms and ≥ 20 % lower RTT) |
| Per-object placement (DO location hints, `locationHint`) | ❌ | — | every Durable Object instance of a workload runs on the workload's host |

## Wrangler config: details

Measured with `e2e/wrangler/testdata/toml-app` (a `wrangler.toml`-only
project) and wrangler 4.60.0 (`make e2e-wrangler`). See
[wrangler.md](wrangler.md).

| Cloudflare / wrangler | tailworks | Tests | Notes |
|---|---|---|---|
| A Workers project with no `tailworks.jsonc` | ✅ defaults | `cmd/tw:TestLoadAppWranglerTOMLOnly`, `cmd/tw:TestCLIDeployWranglerTOML` | workload tag `tag:tw-wl-<workload>`; Durable Object classes internal (`"node": false`), reached through their binding as on Cloudflare |
| `nodejs_compat` through either bundler | ✅ | `e2e/wrangler:TestDevWrangler` | built-in: bare built-ins become `node:*` and workerd provides them; wrangler also inlines unenv polyfills (toml-app's entry: 21 KB against 1.0 KB) |
| `minify`, `keep_names`, `jsx_*`, `tsconfig` | ⚠️ mapped, not measured | — | passed to esbuild's options |
| CommonJS modules, `rules` of type ESModule / CommonJS, Python modules | ❌ | `cmd/tw:TestReadWranglerOutEntry`, `internal/esbundle:TestBuildAppUnsupportedRule` | tailworks ships ES modules (plus text, data, wasm, json) |
| `routes`, `route`, `workers_dev`, `preview_urls`, `account_id` | 🚫 warned | `cmd/tw:TestWranglerWarnings`, `cmd/tw:TestCLIDeployWranglerTOML` | no Cloudflare zone: a workload answers at its MagicDNS name; internet exposure is `tailworks.jsonc` `public` (Funnel) |
| Binding sections with no tailworks binding (`r2_buckets`, `d1_databases`, `services`, `queues`, `ai`, …) | ❌ warned | `cmd/tw:TestWranglerWarnings` | one line per section naming each `env.X` that will be undefined. A section registered as a [platform store](design/adr/044-platform-stores.md) is not warned (`cmd/tw:TestWranglerWarningsStoreKinds`) |
| `assets`, `site` | ❌ warned | `cmd/tw:TestWranglerWarnings` | serve from the Worker or import files as Text/Data modules |
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
| `request.cf.country`, `asn`, `city`… | ❌ absent (no GeoIP in v1) |
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
- **Same-host MCP**: an agent and the MCP server it calls must be on
  different hosts (or the call must go through the platform, not built).
- **Class-instance default exports** (fixed): `export default new
  OAuthProvider(...)` keeps `fetch()` on the prototype, and tailworks' runtime
  copied only own properties, so the Worker had no fetch handler. It now
  finds the handler methods (`fetch`, `scheduled`, `queue`, …) along the
  prototype chain (`@tailworks/tailnet` runtime `wrapHandler`).
- **`request.url` is `http://`**: tailworks hands a Worker the URL it was
  reached at, over plain HTTP inside the tailnet (public routes set
  `X-Forwarded-Proto: https`). Code that compares `request.url`'s origin
  with an HTTPS identifier, such as workers-oauth-provider's
  `resourceMetadata.resource` (the token audience), must present the
  canonical origin. `e2e/sdk/apps/oauthkv` shows how.

## Node process tier (M9)

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
| Thing | Version | Status on the Node tier |
|---|---|---|
| Node.js | 25.1 (macOS), 24.x (Lima) | ✅ (needs ≥ 24 for `NODE_USE_ENV_PROXY`) |
| `workflow` (Vercel Workflow SDK) | 5.4.0 | ✅ `"use workflow"`, steps, `sleep`, `createHook`/`resumeHook`, kill/resume |
| `@workflow/world-local` | 5.2.1 | ✅ (single process per data dir) |
| `@workflow/world-postgres` | 5.1.1 | not tested (follow-up) |
| `@ai-sdk/workflow` `WorkflowAgent` | 2.0.97 (`ai` 7.0.165) | ✅ `generate()` with tools as steps, mock `LanguageModelV4` |
| `undici` | 8.12.4 | ✅ via `dispatcher(undici)` / `installGlobalDispatcher(undici)`; ⚠ importing it disables env proxies |
| Node built-in `fetch` / `http` with env proxies | Node 25 | ✅ (CONNECT / absolute form) |
