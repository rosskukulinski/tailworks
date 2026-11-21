# The Node process tier (opt-in, `"runtime": "node"`)

ADR-022: some agent SDKs need a real Node.js (`node:vm`, child processes,
native modules) and cannot run in workerd. Tailworks runs those as **one supervised
`node` process per deployment with one tailnet identity** (the workload
node). There are no per-object nodes in this tier. A Node process costs
~70 MB (plain app) to ~320 MB (Vercel Workflow + AI SDK) of RSS, against a
few MB per workerd isolate, so it suits a few long-lived services per tenant,
not one per object.

```jsonc
// tailworks.jsonc
{
  "name": "my-svc",
  "runtime": "node",
  "main": "src/server.ts",          // bundled to dist/server.mjs by create-tailworks build-node
  "node": {
    "health": "/healthz",           // any HTTP answer = up (default "/")
    "listen": "tcp",                // tcp: PORT on 127.0.0.1 (default) | unix: TW_SOCKET
    "env": { "LOG_LEVEL": "info" }, // TW_* is reserved
    "args": ["--enable-source-maps"]
  },
  "tailnet": { "tags": ["tag:tw-wl-my-svc"] }
}
```

`tw init my-svc --template node-workflow`, `npm install`, `tw deploy`.
hostd runs `node` from PATH (`tw-hostd -node /path/to/node` or `$TW_NODE`
to pin it). Node ≥ 24 is required for `NODE_USE_ENV_PROXY`.

## Ingress: the verified caller

```
caller ─WG─▶ workload node (netd: WhoIs, strips every inbound x-tw-*/cf-*/forwarded header)
       ─▶ hostd ─▶ app, with  x-tw-caller: v1.<base64url(claims)>.<base64url(HMAC-SHA256(TW_CALLER_KEY, "v1." + claims))>
```

`claims = {v:1, caller:{nodeId,node,hostname,addrs,tags,user?,capMap}, objectId, workload, traceId, iat}`.
`TW_CALLER_KEY` is 32 random bytes, fresh for **every process start**, and
exists only in that process's environment. Verify with `@tailworks/node`:

```ts
import { requireCaller, getCaller, CallerError } from "@tailworks/node";
const claims = requireCaller(req);   // throws CallerError (→ 401 no_identity)
const who = getCaller(req);          // or null
```

A tailnet client can't forge the header, because netd strips it. Another local
process that dials the app's loopback port directly can't produce a valid
signature either: a missing header, a forged one, a wrong key, a skew of more
than 60 s, or a duplicate header are all rejected. Apps must verify; hostd
does not block unauthenticated local connections to the app's port.

## Egress: attributed, ADR-019

Each process gets its own HTTP proxy on `127.0.0.1:<random>`, with Basic
credentials `tw:<token>` that are fresh for every process start. hostd sets:

| env | value |
|---|---|
| `TW_EGRESS_PROXY`, `HTTP_PROXY`, `HTTPS_PROXY` (+ lower case) | `http://tw:<token>@127.0.0.1:<port>` |
| `NODE_USE_ENV_PROXY` | `1` (Node's `fetch()` and `http` honour the proxy vars) |
| `NO_PROXY` | only the app's own port (self-calls, e.g. the Workflow queue) and the proxy itself |

The proxy hands every request to netd, which dials **from the workload node**
under the M1 guard: names resolve only from the node's netmap, and loopback,
self, host IPs, link-local/metadata, 100.100.100.100 and non-tailnet
destinations are refused with a typed `403 forbidden_destination` or
`peer_not_visible`. Requests come in one of two forms:
- absolute form (`GET http://peer/…`): `@tailworks/node` `fetch`, Node `http` with
  env proxies, and an undici `ProxyAgent` with `proxyTunnel:false`. For
  `https://` targets the TLS is originated by netd.
- `CONNECT host:port` tunnels: Node's built-in `fetch()` with env proxies
  uses CONNECT even for `http://`. These are raw TCP from the node's tsnet
  (`netd.Registry.DialEgress`), still guarded and still RST on kill.

Helpers in `@tailworks/node`:
- `fetch(url, init)`: the egress proxy in absolute form.
- `dispatcher(undici)`: an undici `ProxyAgent`.
- `installGlobalDispatcher(undici)`: routes the global `fetch()` through the proxy.

**Undici pitfall (measured with Node 25.1 and undici 8.12):** importing the
`undici` npm package installs a plain `Agent` as the global dispatcher. Every
later global `fetch()` in the process then **silently bypasses**
`NODE_USE_ENV_PROXY` and goes straight to the host network. `make test-node`
reached 1.1.1.1 this way before the fix. If your app (or a dependency) loads
undici, call `installGlobalDispatcher(undici)` afterwards, or use
`@tailworks/node`'s `fetch`. The Vercel Workflow bundles include their own copy of
undici, so the template uses `@tailworks/node` `fetch` explicitly.

### Enforcement boundary (read this before sharing a host)

**On Linux with Landlock (ABI ≥ 4, kernel 6.7+; Ubuntu 24.04's 6.8 has
ABI 4), egress through the proxy is enforced, not cooperative.** tw-hostd
starts every Node app under its Landlock sandbox (`internal/procsb`, L2):
the app may bind and connect only its own app port (TCP listen mode) and
connect only to its egress proxy's port. Every other TCP `connect()`, to
loopback, the LAN or the internet, fails with `EACCES` before a packet is
sent, and a seccomp filter refuses UDP, ICMP, raw, MPTCP and SCTP sockets.
So the undici pitfall above, an own dispatcher, or `net.connect` can no
longer reach anything except through netd's proxy, which attributes it to
this deployment and applies the ADR-019 guard. `make test-node` checks this
from inside the app: `net.connect` to 1.1.1.1:80 and :443 and to a
host-loopback listener all fail (`EACCES`); its own port works; egress via
the proxy is attributed as before (measured on Ubuntu 24.04, kernel 6.8,
ABI 4). Unix sockets: on ABI < 9 Landlock can't refuse a connect to a
pathname socket, so tailworks' own sockets (hostd's, mgmt's, the instance
egress sockets) refuse sandboxed peers themselves; see INSTALL.md.

**On macOS it is enforced too.** The Seatbelt profile refuses every
network operation except TCP connects to `localhost:<proxy port>` and
`localhost:<own port>`, a bind of its own port, and unix sockets in its own
dirs; `make test-node` checks the same probes there (`EPERM`).

**Elsewhere it is cooperative.** On Linux kernels with Landlock ABI < 4
(and under `tw-hostd -sandbox seatbelt-legacy`), a malicious or
buggy Node app can ignore the env proxies, override the global dispatcher,
or open raw sockets to loopback, the LAN, the internet or other local
services, because it is an ordinary process with the host user's network.
`tw doctor` says which case applies. What the process tier guarantees
everywhere:
- the app holds no tailnet key;
- it can't speak as any node except through the proxy, which attributes
  everything to this deployment;
- it can't forge an inbound caller.

`@tailworks/node` closes the *accidental* bypasses in-process: `enforceEgress()`
(also run by `installGlobalDispatcher()`; the `node-workflow` template calls
it at startup) patches `net`/`tls` so that any TCP connect to something
other than the proxy or the app's own port becomes a CONNECT tunnel through
the proxy (so netd's ADR-019 guard still applies and the destination still
sees this deployment's node), makes direct `dns.lookup`/`dns.resolve*` and
UDP sockets fail with `forbidden_destination`, and refuses unix sockets
other than the app's own. It runs inside the app's process, so hostile code
can undo it; it is defence in depth, not a boundary. Verified callers:
`x-tw-caller` now carries a `nonce` and `@tailworks/node` refuses a replayed
header (RT-05), plus `callerObjectId`/`lim` when the caller is a tailworks object
whose caps were narrowed by its node certificate (RT-01).

hostd makes the state visible and refuses the dangerous combination:
- `tw status` and `GET /v1/status` show `egress unenforced` for every
  Node-tier workload on such a host; `tw doctor` warns (`node_egress`);
- a Node-tier deployment on a host **without any process sandbox** that
  serves other tenants fails with `egress_unenforced` (and the general
  shared-host rule, `sandbox_unavailable`, applies to every process-tier
  workload there) unless the operator starts the host with
  `tw up --insecure-shared-host` (`tw-hostd -insecure-shared-host`).
  Under Seatbelt or Landlock the deployment runs; status stays honest about
  whether egress is enforced (`enforced` only under Landlock ABI ≥ 4).

The strongest boundary is the **microVM tier**. There, the guest's only
network is hostd's gVisor gateway, which exposes only the egress, workload
and host-API ports, and every other SYN is RST'd (M8). Running Node apps
inside the Firecracker guest is not built yet; see STATUS.md M9.

### Linux sandbox limits for the app's own processes

Under the Linux sandbox (Landlock + seccomp) the app runs in its own process
group and may send signals only to **itself (`process.pid`) or its own
process group** (`process.kill(0, …)` / `-pgid`), never to hostd, mgmt or
other tenants' processes. The filter names the app's pid, so this reaches
**child processes** too: a child can't `raise()`/`abort()` or signal its own
pid, and the parent can't signal a child *by pid* (`child.kill()` fails
with `EPERM`); signalling the group works. Child processes also get no
`/proc/self` of their own. Apps in TCP listen mode (the default) can't open
any AF_UNIX socket; `listen: "unix"` apps keep AF_UNIX, and a host with
such an app and a reachable system tailscaled socket is not shareable (see
INSTALL.md `host_sockets`).

## Supervision and logs

- **Restarts.** On exit, the app restarts with exponential backoff (250 ms,
  doubling to 10 s, reset after 30 s up). It never gives up. While it is down,
  requests get a typed `503 upstream_unavailable`.
- **Secrets.** Each start gets new secrets.
- **Shutdown.** The process runs in its own process group, and stop is
  SIGTERM, then SIGKILL after 3 s, for the whole group.
- **Logs.** stdout and stderr go to `<instance>/app.log`; read them with
  `tw logs <workload>` (`-f` to follow).
- **Status.** `GET /v1/status` shows `Runtime: "node"`, `Node.{PID, Starts,
  ReadyMs, LogFile, DataDir}`.
- **Environment.** The app does not inherit hostd's environment. It gets
  `PATH`, `HOME`/`TW_DATA_DIR` (persistent:
  `<data>/tenants/<t>/workloads/<w>/node`), `TMPDIR`, `NODE_ENV=production`,
  the variables above, `TW_WORKLOAD`, `TW_DEPLOYMENT` and
  `TW_NODE_NAME`, plus `node.env` and `tw deploy --var`, and the
  workload's secrets (`tw secret put`, [secrets.md](secrets.md)) on top of
  those. Secrets are loaded into `process.env` by a `--require` preload,
  not passed in the environment, so they aren't visible to `ps -E`.
- **Deploys** are **stop-then-start**, with requests held at the gate.
  Durable runtimes such as Workflow's local World and SQLite files assume one
  process per data dir, so blue/green overlap is not used here.

## Vercel Workflow on the Node tier

The `node-workflow` template is a plain `node:http` server with a durable
`"use workflow"` function (`workflows/support.ts`). In that function:
1. An AI SDK `WorkflowAgent` with a scripted mock `LanguageModelV4` (no LLM,
   no keys) calls two tailworks MCP tools over the tailnet: `lookup_order` and
   `refund`, on the `mcp` template.
2. The function sleeps durably (`sleep("3s")`).
3. It waits on a human-approval hook (`createHook`, resumed by
   `POST /hooks/<token>`).

- **World.** `@workflow/world-local`, the filesystem World, in
  `$TW_DATA_DIR/workflow-data`. The SDK documents it for "local development
  and testing", with an in-memory queue and single process per data dir.
  `start()` re-enqueues active runs, which is what makes kill/restart resume
  work. For self-hosted production, the SDK's supported option is
  `@workflow/world-postgres` (pg + graphile-worker). It is not used in the
  hermetic tests (no Postgres in Lima); see the gaps.
- **Build.** `create-tailworks build-node` runs the SDK's `StandaloneBuilder` (the
  builder behind `workflow build`) for `workflows/`. It copies `flow.mjs`, the
  combined workflow and step route bundle (~22 MB), and esbuilds the server
  with the SDK's step-mode SWC transform, so `start(fn)` sees `fn.workflowId`.
  This is what the Nitro, Vite and rollup integrations do.
- **Recovery.** The server must call `(await getWorld()).start()` at boot;
  otherwise in-flight runs are not recovered.
- **Model.** The mock model crosses step boundaries, so it implements the
  SDK's class serialization (`Symbol.for("workflow-serialize")`).
- **Durability.** `make test-node` kills the process with SIGKILL during the
  sleep and again while the run waits on the hook. hostd restarts it, and the
  run completes with steps from three different PIDs.

## Compatibility (M9)

| Thing | Version | Status on the Node tier |
|---|---|---|
| Node.js | 25.1 (macOS), 24.x (Lima) | ✅ (needs ≥ 24 for `NODE_USE_ENV_PROXY`) |
| `workflow` (Vercel Workflow SDK) | 5.5.0 | ✅ `"use workflow"`, steps, `sleep`, `createHook`/`resumeHook`, kill/resume |
| `@workflow/world-local` | 5.3.0 | ✅ (single process per data dir) |
| `@workflow/world-postgres` | 5.1.1 | not tested (follow-up) |
| `@ai-sdk/workflow` `WorkflowAgent` | 2.0.105 (`ai` 7.0.173) | ✅ `generate()` with tools as steps, mock `LanguageModelV4` |
| `undici` | 8.12.4 | ✅ via `dispatcher(undici)` / `installGlobalDispatcher(undici)`; ⚠ importing it disables env proxies |
| Node built-in `fetch` / `http` with env proxies | Node 25 | ✅ (CONNECT / absolute form) |
