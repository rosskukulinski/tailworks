# tailworks

**tailworks is a self-hosted, tailnet-native runtime for Cloudflare-style Workers and
Durable Objects.** You run it on your own machines (macOS or Linux). It runs
your Workers code on [workerd](https://github.com/cloudflare/workerd). Every
[workload](docs/concepts.md#workload) (Cloudflare calls it a Worker), and
**every Durable Object instance**, becomes its own node on your Tailscale
tailnet, with its own MagicDNS name, tags and grants (the words are
explained in [docs/concepts.md](docs/concepts.md)):
- callers are identified by WireGuard identity (`getCaller`), not tokens;
- outbound `fetch()` leaves from the object's own node, so your tailnet
  policy governs what each object can reach;
- `tw kill` cuts an object off in under a second.

tailworks is a community project and is not affiliated with or endorsed by
Tailscale Inc. Tailscale is a registered trademark of Tailscale Inc.

It runs Cloudflare Agents SDK agents, MCP servers and Vercel AI SDK agents
unmodified. What works and what doesn't, surface by surface:
[Compatibility at a glance](#compatibility-at-a-glance). All the docs, by
role and by task: [docs/README.md](docs/README.md).

## Quick start: deploy your first Durable Object

Only want to see the code first? With Node.js ≥ 20 and nothing else,
scaffold an app from npm and type-check it:

```sh
npm create tailworks@latest hello            # also: --template agent, mcp, ai-agent, lock, …
cd hello && npm install && npx tsc --noEmit  # the @tailworks/* packages and the Workers types
```

To run it, install tailworks (step 1), then `npm run dev` (that is,
`tw dev`) serves the app on `127.0.0.1:8787` with no tailnet and no
credentials ([Try it without a tailnet](docs/INSTALL.md#try-it-without-a-tailnet)).

### Prerequisites

| To | You need |
|---|---|
| Run tailworks (the release) | macOS 14 or later on Apple silicon, or Linux arm64/amd64 with glibc ≥ 2.35 ([details](docs/INSTALL.md)); a Tailscale tailnet where you're an admin, or a self-hosted Headscale server ([docs/headscale.md](docs/headscale.md)). No Go and no Node.js. |
| Scaffold an app and type-check it (`npm create tailworks`) | Node.js ≥ 20 only. |
| Try an app locally (`tw dev`) | The release only: no tailnet, no credentials. |
| Build tailworks from source | Go ≥ 1.27 and Node.js ≥ 20. |
| Run a Node.js app (`"runtime": "node"`, the `node-workflow` template) | Node.js ≥ 24 on each host that runs it, and `npm install` on the deploying machine ([docs/node-tier.md](docs/node-tier.md)). |
| Use the Linux microVM tier | `/dev/kvm`, cgroups v2, systemd and a one-time sudo setup ([details](docs/INSTALL.md#linux-the-microvm-tier-firecracker-kvm)). |

Windows isn't supported, not even as a CLI client.

**1. Install** (no sudo; everything goes into `~/.tailworks`). Download the latest
[release](https://github.com/rosskukulinski/tailworks/releases), verify its
checksum and install it. Read [scripts/get.sh](scripts/get.sh) first; it's
short:

```sh
curl -fsSLO https://raw.githubusercontent.com/rosskukulinski/tailworks/master/scripts/get.sh
sh get.sh                                      # or a given release: sh get.sh vX.Y.Z
export PATH="$HOME/.tailworks/bin:$PATH"             # add this to your shell profile too
tw doctor                                     # every check prints a fix if it fails
```

Or build the release yourself (Go and Node.js, as in
[Prerequisites](#prerequisites); the installed tailworks needs neither):

```sh
git clone https://github.com/rosskukulinski/tailworks && cd tailworks
make release RELEASE_TARGETS=$(go env GOOS)-$(go env GOARCH)   # → dist/tailworks-<version>-<os>-<arch>.tar.gz
mkdir -p /tmp/tailworks && tar -xzf dist/tailworks-*-$(go env GOOS)-$(go env GOARCH).tar.gz -C /tmp/tailworks
/tmp/tailworks/tailworks-*/install.sh
```

**2. Let tailworks mint keys on your tailnet.** In the admin console:
- add this to your **tailnet policy**:
  ```jsonc
  "tagOwners": {
    "tag:tw":      ["autogroup:admin"],
    "tag:tw-mgmt": ["autogroup:admin", "tag:tw"],
    "tag:tw-host": ["autogroup:admin", "tag:tw"],
  },
  "grants": [
    {"src": ["tag:tw-host"], "dst": ["tag:tw-mgmt"], "ip": ["tcp:443", "tcp:80"]},
    {"src": ["tag:tw-mgmt"], "dst": ["tag:tw-host"], "ip": ["tcp:7474"]},
    {"src": ["autogroup:admin"], "dst": ["tag:tw-mgmt"], "ip": ["tcp:443", "tcp:80"], "app": {"github.com/rosskukulinski/tailworks/cap/admin": [{}]}},
  ],
  ```
- then generate an **OAuth client**: Settings › OAuth clients, with tag
  `tag:tw` and these scopes:
  - **Auth Keys › Write**: required;
  - **Devices › Core › Write**: so kills also delete the device;
  - **Policy File › Read**: so deploys check your grants first;
  - **Services › Write**: so workloads with `tailnet.service` get a
    Tailscale Service ([docs/services.md](docs/services.md)).

  Only the first is required, but pick all four now. Adding a scope later
  means generating a new client. If you want `tw policy --apply` to write
  each app's policy lines for you (step 4), pick **Policy File › Write**
  instead of Read. That scope can edit your whole policy file, though
  tailworks itself only ever adds `tag:tw*` lines.

Save the client where only you can read it:

```sh
mkdir -p ~/.config/tailworks
printf 'TS_OAUTH_CLIENT_ID=%s\nTS_OAUTH_CLIENT_SECRET=%s\n' '<client id>' '<client secret>' > ~/.config/tailworks/oauth.env
chmod 600 ~/.config/tailworks/oauth.env
```

**3. Start tailworks.** One process: the control plane (`tw-mgmt`) with the host
agent embedded in it, joining your tailnet as a single tagged node
(`tw-mgmt`). No browser login is needed:

```sh
tw up --oauth-client-file ~/.config/tailworks/oauth.env --service --yes
```

It prints the **admin console**, `http://tw-mgmt.<tailnet>.ts.net/ui/`:
open it from any tailnet admin's browser (WhoIs identity, no login) to see
hosts, deployments, every object's lineage and the audit trail, and to kill
or undeploy. Need more machines later? `tw host add` joins another host;
`tw up --separate-host` keeps mgmt and the host agent as two nodes.

**4. Create and deploy a Durable Object app.** The `agent` template is a
Cloudflare Agents SDK `Agent`. Each instance is a Durable Object with its own
SQLite storage, **and** its own tailnet node.

```sh
tw init support --template agent              # also: hello, mcp, ai-agent, chat-agent, pi-agent, lock, refund-approval, node-workflow
cd support
npm install                                   # optional (needs Node.js): editor types for the Agents SDK and ctx.tailnet
tw policy                                     # what this app needs in your tailnet policy, as one snippet
tw policy --apply                             # or: let tailworks add it (needs Policy File › Write)
tw deploy                                     # bundles, uploads, waits until the node is ready
```

`npm create tailworks@latest support -- --template agent` does the same
(it runs `tw init` when `tw` is on your PATH). `tw init` names the app's
tags after it (`tag:tw-wl-support`,
`tag:tw-support-support-agent`, …). `tw policy` checks your tailnet
policy for everything the app needs and prints **one snippet** with what's
missing: the `tagOwners` for its tags, a grant for each `tailnet.connects`,
and the grant that lets people call it (step 5). Paste it into the policy
once, then deploy. Or run `tw policy --apply`: it prints the lines it will
add, asks (`--yes` skips the question), and writes them with `If-Match`.
It only adds missing `tag:tw*` lines, never changes or removes one, and
leaves the step 5 grant to you. `tw deploy --apply-policy` does the same
during a deploy. `tw deploy` runs the same check first and stops if
something is still missing. Every key of `tailworks.jsonc` is in
[docs/tailworks-jsonc.md](docs/tailworks-jsonc.md), and `tw dev` / `tw deploy`
refuse a key they don't know. When it's done, it prints the workload's address,
e.g. `http://tw-support-<id>.<tailnet>.ts.net/` (`<id>` is a short hash;
copy the URL from the `✓ deployed` line).

**5. Let people call it.** Nothing on your tailnet can reach a new workload
until a grant allows it (unless your policy still allows everything). Add
one for the workload's tag; workload nodes listen on port 80. `tw policy`
includes this line in its snippet:

```jsonc
"grants": [
  // everyone in the tailnet may call the support app
  {"src": ["autogroup:member"], "dst": ["tag:tw-wl-support"], "ip": ["tcp:80"]},
]
```

`autogroup:member` is every user of the tailnet. Name a user or group to
narrow it, or a tag to let a tagged device (a server, another workload)
call it. Without the grant, the `curl` below hangs or can't resolve the
name.

**6. Call it** from any device on your tailnet. Each name is a separate
Durable Object instance. The JSON reply shows its verified `caller` (your
tailnet identity) and its persisted `state.visits`:

```sh
curl http://tw-support-<id>.<tailnet>.ts.net/agents/support-agent/alice
curl http://tw-support-<id>.<tailnet>.ts.net/agents/support-agent/alice   # same object: its visit count goes up
tw ls                                         # deployments and objects (each with its own node)
tw audit <object-id>                          # who created, called, spawned or killed it
tw kill <object-id> --cascade                 # cut it (and its sub-agents) off the tailnet
```

No answer, `identity_pending`, or a deployment `failed` in `tw ls`? See
[docs/troubleshooting.md](docs/troubleshooting.md).

## Using tailworks with a coding agent

tailworks ships skills that teach coding agents (Claude Code, Codex, Cursor,
Gemini CLI, OpenCode) to drive the CLI: scaffold an app, run it with
`tw dev`, deploy it, read its logs, debug a failing deploy, draft the
tailnet policy it needs, and stop it. Install them for the agents on your
machine:

```sh
tw agent setup --dry-run      # shows every file it would write
tw agent setup                # asks first; tw agent remove undoes it
```

Then ask your agent something like "build me an MCP server and run it on
tw". The skills are built into the CLI (`tw agent show <skill>` prints
one), every command takes `--json` with a documented schema, and errors carry
a code and a hint. The skills tell agents to leave credentials and tailnet
policy changes to you, and to ask before anything destructive.

Agents can also operate tailworks over MCP: `claude mcp add tw -- tw mcp`
(`tw mcp --config` prints the setup for others). Read tools work for
anyone you grant; `kill`, `undeploy` and `deploy` only for callers whose
tailnet grant names them. Details: [docs/agents.md](docs/agents.md).

## Compatibility at a glance

What of Cloudflare Workers works on tailworks. ✅ works, tested · ⚠️ partial or
different · ❌ not supported yet · 🚫 not supported, by design. Each row links
to its detail in [docs/COMPAT.md](docs/COMPAT.md), which names the tests that
prove every ✅ and ⚠️.

| Area | Surface | Status | Note |
|---|---|---|---|
| Runtime | [`fetch` handler, outbound `fetch()`](docs/COMPAT.md#fetch) | ⚠️ | outbound reaches tailnet peers from the workload's (or object's) own node, and public internet hosts from the host's network (on by default; `tailnet.egress.internet: false` turns it off, [call an internet API](docs/recipes.md#call-an-internet-api)) |
| Runtime | [WebSockets](docs/COMPAT.md#websockets) | ✅ | |
| Runtime | [TCP sockets (`connect()`)](docs/COMPAT.md#connect) | ❌ | egress is HTTP only |
| Runtime | [WebAssembly](docs/COMPAT.md#wasm) | ✅ | |
| Runtime | [`nodejs_compat`](docs/COMPAT.md#nodejs-compat) | ✅ | |
| Runtime | [Python Workers](docs/COMPAT.md#python) | ❌ | Python runs in containers |
| Runtime | [Node.js apps (`runtime: "node"`)](docs/COMPAT.md#node-runtime) | ✅ | tw-only |
| Durable Objects | [One tailnet node per object](docs/COMPAT.md#do-identity) | ✅ | tw-only: own name, tags, grants, egress |
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
| Bindings | [`tw d1` commands](docs/COMPAT.md#d1-cli) | ⚠️ | reach the running workload |
| Bindings | [Queues](docs/COMPAT.md#queues) | ✅ | queues live in mgmt; producer and consumer on any hosts |
| Bindings | [Workflows](docs/COMPAT.md#workflows) | ✅ | workerd's built-in engine; approval events are tailworks' |
| Bindings | [Workers AI](docs/COMPAT.md#workers-ai) | ❌ | use an LLM gateway on your tailnet |
| Bindings | [Vectorize](docs/COMPAT.md#vectorize) | ❌ | |
| Bindings | [Hyperdrive](docs/COMPAT.md#hyperdrive) | ❌ | |
| Bindings | [Browser Rendering](docs/COMPAT.md#browser) | ❌ | |
| Bindings | [Email](docs/COMPAT.md#email) | ❌ | |
| Bindings | [Service bindings](docs/COMPAT.md#service-bindings) | ❌ | call the workload over the tailnet |
| Bindings | [Secrets](docs/COMPAT.md#secrets) | ⚠️ | `tw secret`; applied on the next deploy |
| Bindings | [Vars](docs/COMPAT.md#vars) | ✅ | |
| Bindings | [Static assets](docs/COMPAT.md#assets) | ❌ | |
| Triggers | [Cron](docs/COMPAT.md#cron) | ✅ | |
| Triggers | [Queue consumers](docs/COMPAT.md#queue-consumers) | ⚠️ | competing consumers allowed; fixed concurrency |
| Triggers | [Queue pull consumers](docs/COMPAT.md#queue-pull) | ⚠️ | tailworks' API, admin only |
| Platform | [Caller identity and auth](docs/COMPAT.md#identity) | 🚫 | `getCaller` from WireGuard, not tokens or Access JWTs |
| Platform | [Workflow approvals by tailnet identity](docs/COMPAT.md#approvals) | ✅ | tw-only: `tw approve`, decided under `github.com/rosskukulinski/tailworks/cap/approve` grants |
| Platform | [Placement](docs/COMPAT.md#placement) | ⚠️ | at deploy, by tailnet path; never moves on its own |
| Platform | [Public routes](docs/COMPAT.md#public-routes) | ⚠️ | opt-in paths over Funnel; tailnet-only by default |
| Platform | [Containers, Sandbox SDK](docs/COMPAT.md#containers) | ⚠️ | one microVM per container |
| Platform | [Logs and tail](docs/COMPAT.md#logs) | ⚠️ | `tw logs`: a structured line per request; no Tail Workers |
| Platform | [Metrics](docs/COMPAT.md#metrics) | ⚠️ | Prometheus `/metrics` on the tailnet, and OTLP; no CPU time |
| Platform | [Tracing (`traceparent`)](docs/COMPAT.md#traces) | ⚠️ | across DO calls and egress; not DO RPC |
| Platform | [Export to OpenTelemetry](docs/COMPAT.md#otlp-export) | ⚠️ | `tw telemetry set`; collector on the tailnet |
| Platform | [Wrangler config (`wrangler.toml`/`.json`/`.jsonc`)](docs/COMPAT.md#wrangler-config) | ⚠️ | keys tailworks can't use are warned, one line each |
| Platform | [Environments (`--env`)](docs/COMPAT.md#wrangler-env) | ✅ | one workload per environment |
| Platform | [Bundling (`define`, `alias`, `rules`, `--wrangler`)](docs/COMPAT.md#wrangler-bundling) | ✅ | built-in esbuild, or wrangler's own |
| Platform | [`wrangler deploy` / `secret` / `tail`](docs/COMPAT.md#wrangler-api) | 🚫 | use `tw deploy` / `secret` / `logs` |
| SDKs | [Cloudflare Agents SDK](docs/COMPAT.md#agents-sdk) | ⚠️ | core and `AgentWorkflow` run unmodified; no `subAgent()` |
| SDKs | [MCP servers](docs/COMPAT.md#mcp) | ✅ | tools gated by tailnet grants |
| SDKs | [Vercel AI SDK](docs/COMPAT.md#vercel-ai) | ⚠️ | MCP client needs `mcpFetch` |
| Tooling | [CLI `--json` output](docs/COMPAT.md#cli-json) | ✅ | schema for every command; NDJSON events |
| Tooling | [Coding-agent skills (`tw agent`)](docs/COMPAT.md#agent-skills) | ✅ | Claude Code, Codex, Cursor, Gemini CLI, OpenCode |
| Tooling | [Testing: `@cloudflare/vitest-plugin`](docs/COMPAT.md#vitest-plugin) | ⚠️ | Worker and DO logic as on Cloudflare; callers and `ctx.tailnet` via `@tailworks/tailnet/testing` |
| Tooling | [tailworks' MCP server (`/mcp`, `tw mcp`)](docs/COMPAT.md#tw-mcp) | ✅ | tools gated per tool by a tailnet grant |
| Tooling | [Docs for agents (`llms.txt`, `tw docs`)](docs/COMPAT.md#docs-for-agents) | ✅ | offline search |

## What tw sends

tailworks has no telemetry. The one request the CLI makes on its own is an
update check: at most once a day, after an interactive command (a release
build, stderr on a terminal, no `--json`, `CI` unset), `tw` asks
`api.github.com` for the latest release of this repository, sending only
the request itself with a `tw/<version>` User-Agent, and caches the answer
in `~/.tailworks/update-check.json`. It never runs after `tw init` or
`tw dev`, which make no outbound request. `TW_NO_UPDATE_CHECK=1` turns it
off; `tw version --json` reports it as `updateCheck`. Details:
[docs/security-model.md](docs/security-model.md#telemetry-and-the-update-check).

## More

- **Docs map** (every doc by role and by task): [docs/README.md](docs/README.md).
- **CLI reference:** [docs/cli.md](docs/cli.md), every command and flag
  (generated from `--help`); `tw help` groups the commands.
- **What changed in each release:** [CHANGELOG.md](CHANGELOG.md).
  Security reports: [SECURITY.md](SECURITY.md). Contributing:
  [CONTRIBUTING.md](CONTRIBUTING.md).
- **The words tailworks uses** (tenant, workload, tier, facet mode, pool node,
  …): [docs/concepts.md](docs/concepts.md).
- **A first deploy that doesn't answer:**
  [docs/troubleshooting.md](docs/troubleshooting.md).
- **Install details and troubleshooting:**
  [docs/INSTALL.md](docs/INSTALL.md); where each platform is tested, and
  the measured timings: [docs/install-testing.md](docs/install-testing.md).
  - [Upgrade](docs/upgrading.md),
    [back up and restore](docs/INSTALL.md#back-up-and-restore) and
    [uninstall](docs/INSTALL.md#uninstall).
  - Upgrading a tailworks that minted with an API key or WIF: switch it to an
    [OAuth client](docs/INSTALL.md#upgrading-from-api-key-or-wif-mode).
  - A separate developer laptop: `tw link`.
  - Adding hosts: `tw host add`.
  - The Linux microVM tier (Firecracker).
- **Serve chosen paths to the public internet** (webhooks, OAuth callbacks)
  over Funnel: [docs/public-ingress.md](docs/public-ingress.md).
- **API keys and other secrets** (`env.NAME`, like `wrangler secret`):
  `tw secret put`, and `.dev.vars` for `tw dev`. See
  [docs/secrets.md](docs/secrets.md).
- **Run an app locally with no tailnet at all:** `tw dev`.
- **`tailworks.jsonc`, every key:** [docs/tailworks-jsonc.md](docs/tailworks-jsonc.md)
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
  leases, fencing tokens and what tailworks guarantees when a host fails:
  [docs/locks.md](docs/locks.md) and `tw init --template lock`.
- **Queues** (`queues.producers` → `env.Q.send()`, `queues.consumers` →
  `queue()`, retries, dead-letter queues, `tw queues`):
  [docs/queues.md](docs/queues.md).
- **Durable Workflows that wait for a person's approval**, decided by a
  tailnet identity under a `github.com/rosskukulinski/tailworks/cap/approve` grant (`tw approve`):
  [docs/approvals.md](docs/approvals.md).
- **Logs, metrics and traces:** `tw logs <workload>` shows one structured
  line per request (`--json`, `--errors`, …), hosts serve Prometheus
  `/metrics` on their tailnet node, and `tw telemetry set --otlp …`
  exports logs, metrics and traces to your OpenTelemetry collector. See
  [docs/observability.md](docs/observability.md).
- **mgmt's API:** [api/openapi.yaml](api/openapi.yaml), rendered in
  [docs/contracts/mgmt-api.md](docs/contracts/mgmt-api.md).
- **The admin console:** [docs/ADMIN-UI.md](docs/ADMIN-UI.md).
- **Every typed error code and its fix:** [docs/errors.md](docs/errors.md).
- **Scripting tailworks, or driving it from a coding agent:** `--json` output and
  its schema in [docs/cli-json.md](docs/cli-json.md); skills and
  `tw agent setup` in [docs/agents.md](docs/agents.md); an index of these
  docs for agents in [llms.txt](llms.txt), and `tw docs search` offline.
- **Security:** [security/THREAT-MODEL.md](security/THREAT-MODEL.md) and
  [security/RED-TEAM.md](security/RED-TEAM.md) (the attacker's view,
  findings and fixes).
- **Running several mgmt replicas on Postgres:** the chaos and soak harness
  and its findings are in [e2e/multimgmt/](e2e/multimgmt/)
  (`LEDGER.md`). Replicas can share one Tailscale Service VIP
  (`-tsnet-service svc:tw-mgmt`).
- **Stable names for workloads (Tailscale Services):** `"service": true` in
  `tailworks.jsonc`; stateless workloads can also run on several hosts behind it
  (`"replicas": 3`). See [docs/services.md](docs/services.md).
- **Build status and measured results:** [status/](status/) (one file per workstream) and the frozen history in [STATUS.md](STATUS.md).
- **Design:** the RFC is [docs/design/RFC.md](docs/design/RFC.md) and the
  ADRs are in [docs/design/adr/](docs/design/adr/), one file each.
