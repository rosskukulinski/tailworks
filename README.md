# twd

**twd is a self-hosted, tailnet-native runtime for Cloudflare-style Workers and
Durable Objects.** You run it on your own machines (macOS or Linux). It runs
your Workers code on [workerd](https://github.com/cloudflare/workerd). Every
workload, and **every Durable Object instance**, becomes its own node on your
Tailscale tailnet, with its own MagicDNS name, tags and grants:
- callers are identified by WireGuard identity (`getCaller`), not tokens;
- outbound `fetch()` leaves from the object's own node, so your tailnet
  policy governs what each object can reach;
- `twd kill` cuts an object off in under a second.

It runs Cloudflare Agents SDK agents, MCP servers and Vercel AI SDK agents
unmodified. What works and what doesn't, surface by surface:
[Compatibility at a glance](#compatibility-at-a-glance). All the docs, by
role and by task: [docs/README.md](docs/README.md).

## Quick start: deploy your first Durable Object

Only want to run the code first? After step 1, `twd init hello && cd hello
&& twd dev` serves the app on `127.0.0.1:8787` with no tailnet and no
credentials ([Try it without a tailnet](docs/INSTALL.md#try-it-without-a-tailnet)).

You need:
- a Mac (Apple silicon) or a Linux machine (arm64 or amd64);
- a Tailscale tailnet where you're an admin (or a self-hosted Headscale
  server: see [docs/headscale.md](docs/headscale.md)).

**1. Install** (no sudo; everything goes into `~/.twd`). Download the latest
[release](https://github.com/rosskukulinski/twd/releases), verify its
checksum and install it. Read [scripts/get.sh](scripts/get.sh) first; it's
short:

```sh
curl -fsSLO https://raw.githubusercontent.com/rosskukulinski/twd/master/scripts/get.sh
sh get.sh                                      # or: sh get.sh v0.1.0
export PATH="$HOME/.twd/bin:$PATH"             # add this to your shell profile too
twd doctor                                     # every check prints a fix if it fails
```

Or build the release yourself (needs Go ≥ 1.27 and Node.js ≥ 20; the
installed twd needs neither):

```sh
git clone https://github.com/rosskukulinski/twd && cd twd
make release RELEASE_TARGETS=$(go env GOOS)-$(go env GOARCH)   # → dist/twd-<version>-<os>-<arch>.tar.gz
mkdir -p /tmp/twd && tar -xzf dist/twd-*-$(go env GOOS)-$(go env GOARCH).tar.gz -C /tmp/twd
/tmp/twd/twd-*/install.sh
```

**2. Let twd mint keys on your tailnet.** In the admin console:
- add this to your **tailnet policy**:
  ```jsonc
  "tagOwners": {
    "tag:twd":      ["autogroup:admin"],
    "tag:twd-mgmt": ["autogroup:admin", "tag:twd"],
    "tag:twd-host": ["autogroup:admin", "tag:twd"],
  },
  "grants": [
    {"src": ["tag:twd-host"], "dst": ["tag:twd-mgmt"], "ip": ["tcp:443", "tcp:80"]},
    {"src": ["tag:twd-mgmt"], "dst": ["tag:twd-host"], "ip": ["tcp:7474"]},
    {"src": ["autogroup:admin"], "dst": ["tag:twd-mgmt"], "ip": ["tcp:443", "tcp:80"], "app": {"twd.dev/cap/admin": [{}]}},
  ],
  ```
- then generate an **OAuth client**: Settings › OAuth clients, with tag
  `tag:twd` and these scopes:
  - **Auth Keys › Write**: required;
  - **Devices › Core › Write**: so kills also delete the device;
  - **Policy File › Read**: so deploys check your grants first;
  - **Services › Write**: so workloads with `tailnet.service` get a
    Tailscale Service ([docs/services.md](docs/services.md)).

  Only the first is required, but pick all four now. Adding a scope later
  means generating a new client.

Save the client where only you can read it:

```sh
mkdir -p ~/.config/twd
printf 'TS_OAUTH_CLIENT_ID=%s\nTS_OAUTH_CLIENT_SECRET=%s\n' '<client id>' '<client secret>' > ~/.config/twd/oauth.env
chmod 600 ~/.config/twd/oauth.env
```

**3. Start twd.** One process: the control plane (`twd-mgmt`) with the host
agent embedded in it, joining your tailnet as a single tagged node
(`twd-mgmt`). No browser login is needed:

```sh
twd up --oauth-client-file ~/.config/twd/oauth.env --service --yes
```

It prints the **admin console**, `http://twd-mgmt.<tailnet>.ts.net/ui/`:
open it from any tailnet admin's browser (WhoIs identity, no login) to see
hosts, deployments, every object's lineage and the audit trail, and to kill
or undeploy. Need more machines later? `twd host add` joins another host;
`twd up --separate-host` keeps mgmt and the host agent as two nodes.

**4. Create and deploy a Durable Object app.** The `agent` template is a
Cloudflare Agents SDK `Agent`. Each instance is a Durable Object with its own
SQLite storage, **and** its own tailnet node.

```sh
twd init support --template agent              # also: hello, mcp, ai-agent, lock, refund-approval, node-workflow
cd support
twd deploy                                     # bundles, uploads, waits until the node is ready
```

`twd init` names the app's tags after it (`tag:twd-wl-support`,
`tag:twd-support-support-agent`, …). `twd deploy` checks them first. If
your policy has no `tagOwners` line for them, it prints the exact snippet
to add; add it and run `twd deploy` again. Every key of `twd.jsonc` is in
[docs/twd-jsonc.md](docs/twd-jsonc.md), and `twd dev` / `twd deploy`
refuse a key they don't know. When it's done, it prints the workload's address,
e.g. `http://twd-support-<id>.<tailnet>.ts.net/` (`<id>` is a short hash;
copy the URL from the `✓ deployed` line).

**5. Call it** from any device on your tailnet. Each name is a separate
Durable Object instance. The JSON reply shows its verified `caller` (your
tailnet identity) and its persisted `state.visits`:

```sh
curl http://twd-support-<id>.<tailnet>.ts.net/agents/support-agent/alice
curl http://twd-support-<id>.<tailnet>.ts.net/agents/support-agent/alice   # same object: its visit count goes up
twd ls                                         # deployments and objects (each with its own node)
twd audit <object-id>                          # who created, called, spawned or killed it
twd kill <object-id> --cascade                 # cut it (and its sub-agents) off the tailnet
```

## Using twd with a coding agent

twd ships skills that teach coding agents (Claude Code, Codex, Cursor,
Gemini CLI, OpenCode) to drive the CLI: scaffold an app, run it with
`twd dev`, deploy it, read its logs, debug a failing deploy, draft the
tailnet policy it needs, and stop it. Install them for the agents on your
machine:

```sh
twd agent setup --dry-run      # shows every file it would write
twd agent setup                # asks first; twd agent remove undoes it
```

Then ask your agent something like "build me an MCP server and run it on
twd". The skills are built into the CLI (`twd agent show <skill>` prints
one), every command takes `--json` with a documented schema, and errors carry
a code and a hint. The skills tell agents to leave credentials and tailnet
policy changes to you, and to ask before anything destructive.

Agents can also operate twd over MCP: `claude mcp add twd -- twd mcp`
(`twd mcp --config` prints the setup for others). Read tools work for
anyone you grant; `kill`, `undeploy` and `deploy` only for callers whose
tailnet grant names them. Details: [docs/agents.md](docs/agents.md).

## Compatibility at a glance

What of Cloudflare Workers works on twd. ✅ works, tested · ⚠️ partial or
different · ❌ not supported yet · 🚫 not supported, by design. Each row links
to its detail in [docs/COMPAT.md](docs/COMPAT.md), which names the tests that
prove every ✅ and ⚠️.

| Area | Surface | Status | Note |
|---|---|---|---|
| Runtime | [`fetch` handler, outbound `fetch()`](docs/COMPAT.md#fetch) | ⚠️ | outbound reaches tailnet peers only (no internet, by default) |
| Runtime | [WebSockets](docs/COMPAT.md#websockets) | ✅ | |
| Runtime | [TCP sockets (`connect()`)](docs/COMPAT.md#connect) | ❌ | egress is HTTP only |
| Runtime | [WebAssembly](docs/COMPAT.md#wasm) | ✅ | |
| Runtime | [`nodejs_compat`](docs/COMPAT.md#nodejs-compat) | ✅ | |
| Runtime | [Python Workers](docs/COMPAT.md#python) | ❌ | Python runs in containers |
| Runtime | [Node.js apps (`runtime: "node"`)](docs/COMPAT.md#node-runtime) | ✅ | twd-only |
| Durable Objects | [One tailnet node per object](docs/COMPAT.md#do-identity) | ✅ | twd-only: own name, tags, grants, egress |
| Durable Objects | [Storage (KV API, SQLite)](docs/COMPAT.md#do-storage) | ✅ | one host, no replication |
| Durable Objects | [Alarms](docs/COMPAT.md#alarms) | ✅ | |
| Durable Objects | [Hibernation](docs/COMPAT.md#hibernation) | ✅ | |
| Durable Objects | [Locks / leader election (pattern)](docs/COMPAT.md#locks) | ⚠️ | manual failover; [docs/locks.md](docs/locks.md) |
| Durable Objects | [RPC](docs/COMPAT.md#do-rpc) | ⚠️ | no cross-Worker `script_name` |
| Durable Objects | [Facets, `subAgent()`](docs/COMPAT.md#facets) | ❌ | use `ctx.tailnet.spawn` |
| Bindings | [KV](docs/COMPAT.md#kv) | ⚠️ | per workload; values ≤ 2 MiB |
| Bindings | [R2](docs/COMPAT.md#r2) | ⚠️ | bytes on the host's disk or an S3-compatible service |
| Bindings | [R2 multipart upload](docs/COMPAT.md#r2-multipart) | ⚠️ | unfinished uploads are kept |
| Bindings | [D1](docs/COMPAT.md#d1) | ⚠️ | one workload's database; survives host moves |
| Bindings | [D1 sessions](docs/COMPAT.md#d1-sessions) | ⚠️ | bookmarks are journal positions |
| Bindings | [D1 migrations](docs/COMPAT.md#d1-migrations) | ✅ | wrangler's layout |
| Bindings | [`twd d1` commands](docs/COMPAT.md#d1-cli) | ⚠️ | reach the running workload |
| Bindings | [Queues](docs/COMPAT.md#queues) | ✅ | queues live in mgmt; producer and consumer on any hosts |
| Bindings | [Workflows](docs/COMPAT.md#workflows) | ✅ | workerd's built-in engine; approval events are twd's |
| Bindings | [Workers AI](docs/COMPAT.md#workers-ai) | ❌ | use an LLM gateway on your tailnet |
| Bindings | [Vectorize](docs/COMPAT.md#vectorize) | ❌ | |
| Bindings | [Hyperdrive](docs/COMPAT.md#hyperdrive) | ❌ | |
| Bindings | [Browser Rendering](docs/COMPAT.md#browser) | ❌ | |
| Bindings | [Email](docs/COMPAT.md#email) | ❌ | |
| Bindings | [Service bindings](docs/COMPAT.md#service-bindings) | ❌ | call the workload over the tailnet |
| Bindings | [Secrets](docs/COMPAT.md#secrets) | ⚠️ | `twd secret`; applied on the next deploy |
| Bindings | [Vars](docs/COMPAT.md#vars) | ✅ | |
| Bindings | [Static assets](docs/COMPAT.md#assets) | ❌ | |
| Triggers | [Cron](docs/COMPAT.md#cron) | ✅ | |
| Triggers | [Queue consumers](docs/COMPAT.md#queue-consumers) | ⚠️ | competing consumers allowed; fixed concurrency |
| Triggers | [Queue pull consumers](docs/COMPAT.md#queue-pull) | ⚠️ | twd's API, admin only |
| Platform | [Caller identity and auth](docs/COMPAT.md#identity) | 🚫 | `getCaller` from WireGuard, not tokens or Access JWTs |
| Platform | [Workflow approvals by tailnet identity](docs/COMPAT.md#approvals) | ✅ | twd-only: `twd approve`, decided under `twd.dev/cap/approve` grants |
| Platform | [Placement](docs/COMPAT.md#placement) | ⚠️ | at deploy, by tailnet path; never moves on its own |
| Platform | [Public routes](docs/COMPAT.md#public-routes) | ⚠️ | opt-in paths over Funnel; tailnet-only by default |
| Platform | [Containers, Sandbox SDK](docs/COMPAT.md#containers) | ⚠️ | one microVM per container |
| Platform | [Logs and tail](docs/COMPAT.md#logs) | ⚠️ | `twd logs`: a structured line per request; no Tail Workers |
| Platform | [Metrics](docs/COMPAT.md#metrics) | ⚠️ | Prometheus `/metrics` on the tailnet, and OTLP; no CPU time |
| Platform | [Tracing (`traceparent`)](docs/COMPAT.md#traces) | ⚠️ | across DO calls and egress; not DO RPC |
| Platform | [Export to OpenTelemetry](docs/COMPAT.md#otlp-export) | ⚠️ | `twd telemetry set`; collector on the tailnet |
| Platform | [Wrangler config (`wrangler.toml`/`.json`/`.jsonc`)](docs/COMPAT.md#wrangler-config) | ⚠️ | keys twd can't use are warned, one line each |
| Platform | [Environments (`--env`)](docs/COMPAT.md#wrangler-env) | ✅ | one workload per environment |
| Platform | [Bundling (`define`, `alias`, `rules`, `--wrangler`)](docs/COMPAT.md#wrangler-bundling) | ✅ | built-in esbuild, or wrangler's own |
| Platform | [`wrangler deploy` / `secret` / `tail`](docs/COMPAT.md#wrangler-api) | 🚫 | use `twd deploy` / `secret` / `logs` |
| SDKs | [Cloudflare Agents SDK](docs/COMPAT.md#agents-sdk) | ⚠️ | core and `AgentWorkflow` run unmodified; no `subAgent()` |
| SDKs | [MCP servers](docs/COMPAT.md#mcp) | ✅ | tools gated by tailnet grants |
| SDKs | [Vercel AI SDK](docs/COMPAT.md#vercel-ai) | ⚠️ | MCP client needs `mcpFetch` |
| Tooling | [CLI `--json` output](docs/COMPAT.md#cli-json) | ✅ | schema for every command; NDJSON events |
| Tooling | [Coding-agent skills (`twd agent`)](docs/COMPAT.md#agent-skills) | ✅ | Claude Code, Codex, Cursor, Gemini CLI, OpenCode |
| Tooling | [twd's MCP server (`/mcp`, `twd mcp`)](docs/COMPAT.md#twd-mcp) | ✅ | tools gated per tool by a tailnet grant |
| Tooling | [Docs for agents (`llms.txt`, `twd docs`)](docs/COMPAT.md#docs-for-agents) | ✅ | offline search |

## More

- **Docs map** (every doc by role and by task): [docs/README.md](docs/README.md).
- **CLI reference:** `twd help` lists every command and its flags;
  `twd <command> --help` shows one.
- **Install details, other modes and troubleshooting:**
  [docs/INSTALL.md](docs/INSTALL.md).
  - [Upgrade](docs/upgrading.md),
    [back up and restore](docs/INSTALL.md#back-up-and-restore) and
    [uninstall](docs/INSTALL.md#uninstall).
  - Other ways to mint keys: workload identity federation (`twd up --wif`)
    or an API key.
  - A separate developer laptop: `twd link`.
  - Adding hosts: `twd host add`.
  - The Linux microVM tier (Firecracker).
- **Serve chosen paths to the public internet** (webhooks, OAuth callbacks)
  over Funnel: [docs/public-ingress.md](docs/public-ingress.md).
- **API keys and other secrets** (`env.NAME`, like `wrangler secret`):
  `twd secret put`, and `.dev.vars` for `twd dev`. See
  [docs/secrets.md](docs/secrets.md).
- **Run an app locally with no tailnet at all:** `twd dev`.
- **`twd.jsonc`, every key:** [docs/twd-jsonc.md](docs/twd-jsonc.md)
  (with a JSON Schema for editor completion).
- **An existing Workers project** (`wrangler.toml`, environments, bundling
  with wrangler), and what's different from Cloudflare in short:
  [docs/wrangler.md](docs/wrangler.md).
- **What runs and what doesn't:** [docs/COMPAT.md](docs/COMPAT.md)
  (every Workers surface, with the tests behind it) and
  [docs/node-tier.md](docs/node-tier.md) (Node.js apps).
- **Cron triggers** (`triggers.crons` → `scheduled()`):
  [docs/cron-triggers.md](docs/cron-triggers.md).
- **Locks and leader election** ("only one worker runs this job"), with
  leases, fencing tokens and what twd guarantees when a host fails:
  [docs/locks.md](docs/locks.md) and `twd init --template lock`.
- **Queues** (`queues.producers` → `env.Q.send()`, `queues.consumers` →
  `queue()`, retries, dead-letter queues, `twd queues`):
  [docs/queues.md](docs/queues.md).
- **Durable Workflows that wait for a person's approval**, decided by a
  tailnet identity under a `twd.dev/cap/approve` grant (`twd approve`):
  [docs/approvals.md](docs/approvals.md).
- **Logs, metrics and traces:** `twd logs <workload>` shows one structured
  line per request (`--json`, `--errors`, …), hosts serve Prometheus
  `/metrics` on their tailnet node, and `twd telemetry set --otlp …`
  exports logs, metrics and traces to your OpenTelemetry collector. See
  [docs/observability.md](docs/observability.md).
- **mgmt's API:** [api/openapi.yaml](api/openapi.yaml), rendered in
  [docs/contracts/mgmt-api.md](docs/contracts/mgmt-api.md).
- **The admin console:** [docs/ADMIN-UI.md](docs/ADMIN-UI.md).
- **Every typed error code and its fix:** [docs/errors.md](docs/errors.md).
- **Scripting twd, or driving it from a coding agent:** `--json` output and
  its schema in [docs/cli-json.md](docs/cli-json.md); skills and
  `twd agent setup` in [docs/agents.md](docs/agents.md); an index of these
  docs for agents in [llms.txt](llms.txt), and `twd docs search` offline.
- **Security:** [security/THREAT-MODEL.md](security/THREAT-MODEL.md) and
  [security/RED-TEAM.md](security/RED-TEAM.md) (the attacker's view,
  findings and fixes).
- **Running several mgmt replicas on Postgres:** the chaos and soak harness
  and its findings are in [e2e/multimgmt/](e2e/multimgmt/)
  (`LEDGER.md`). Replicas can share one Tailscale Service VIP
  (`-tsnet-service svc:twd-mgmt`).
- **Stable names for workloads (Tailscale Services):** `"service": true` in
  `twd.jsonc`; stateless workloads can also run on several hosts behind it
  (`"replicas": 3`). See [docs/services.md](docs/services.md).
- **Build status and measured results:** [status/](status/) (one file per workstream) and the frozen history in [STATUS.md](STATUS.md).
- **Design:** the RFC is [docs/design/RFC.md](docs/design/RFC.md) and the
  ADRs are in [docs/design/adr/](docs/design/adr/), one file each.
