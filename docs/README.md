# tw docs map

Every tailworks doc, by who you are and by what you need. The guides in this
directory ship in the CLI: `tw docs ls`, `tw docs search <words>`,
`tw docs show <doc>[#<anchor>]`. The API contract (`contracts/`) and the
design records (`design/`) don't; read them here on GitHub.

## Start here by role

- **You write Cloudflare Workers.** Run the app locally first, with no
  tailnet: [Try it without a tailnet](dev-debugging.md#try-it-without-a-tailnet).
  Then the [quick start](../README.md#quick-start).
  Bringing a wrangler project? Read
  [What's different from Cloudflare, in short](wrangler.md#whats-different-from-cloudflare-in-short),
  then [COMPAT.md](COMPAT.md) for each surface.
- **You run the machines.** [INSTALL.md](INSTALL.md): [macOS](INSTALL.md#macos)
  or [Linux](INSTALL.md#linux), then [upgrading.md](upgrading.md),
  [back up and restore](INSTALL.md#back-up-and-restore) and
  [troubleshooting](troubleshooting.md). More than one machine:
  [hosts.md](hosts.md). Where each platform is
  tested: [install-testing.md](install-testing.md). Everything `tw up` runs,
  listens on and writes, on one page: [footprint.md](footprint.md).
- **You review security.** Start with the one-page [security model](security-model.md); report a vulnerability through [SECURITY.md](../SECURITY.md).
  How callers are identified:
  [COMPAT.md#identity](COMPAT.md#identity). How isolated an app is:
  [Isolation tiers](security-model.md#isolation-tiers) and
  [containers.md](containers.md) (the microVM tier). The attacker's view:
  [THREAT-MODEL.md](../security/THREAT-MODEL.md) and
  [RED-TEAM.md](../security/RED-TEAM.md). The design:
  [RFC](design/RFC.md) and the [ADRs](design/adr/README.md).
- **You are a coding agent.** [agents.md](agents.md), then
  `tw agent setup`. Parse `--json` with [cli-json.md](cli-json.md); look
  up error codes in [errors.md](errors.md). The index for agents is
  [llms.txt](../llms.txt).
- **You want to change tailworks.** [CONTRIBUTING.md](../CONTRIBUTING.md):
  set up, the tests a pull request runs, release-note fragments and status
  files.

## Learn: walk-throughs

- [Quick start](../README.md#quick-start):
  install, start tailworks, deploy an app, call it.
- [Concepts](concepts.md): the words tailworks uses (tenant, workload, object,
  tiers, facet mode, pool nodes, warm keys).
- [Try it without a tailnet](dev-debugging.md#try-it-without-a-tailnet): `tw dev`.
- Templates: `tw init <dir> --template <name>`. Four starting points:
  `hello` (the default), `agent`, `mcp`, `static-site` (wrangler `assets`: a
  folder of files next to a Worker). `tw init --list` shows them all; the
  demos and patterns go with their guides: `refund-approval`
  ([approvals](approvals.md)), `lock` ([locks](locks.md)), `node-workflow`
  ([the Node.js tier](node-tier.md)), `python` ([Python Workers](python.md)),
  `ai-agent` (an AI SDK agent whose tools need the `mcp` template deployed
  as workload `mcp`; [its README](../js/packages/create-tailworks/templates/ai-agent/README.md)),
  `chat-agent` (Cloudflare's `AIChatAgent` chat example, with a chat page)
  and `pi-agent` (Cloudflare's Pi harness example); both use an LLM gateway
  on your tailnet, such as Aperture, when you point them at it.
  Each has a README.md: what it shows, how to run it, what to change.
- [The launch demo](demo.md): `scripts/demo.sh` deploys an MCP server and an
  agent, shows a tool call allowed, then denied by one grant, then the agent
  killed. Also how to record it.
- [Recipes](recipes.md): short, runnable how-tos (internet APIs and LLMs through a tailnet gateway, a Durable Object per user, a signed webhook, cron into KV).

## Do: how-to guides

- Bring a wrangler project: [wrangler.md](wrangler.md)
- Fix `tw up`, a first deploy that doesn't answer, `tw link` or a `tw doctor`
  warning: [troubleshooting.md](troubleshooting.md)
- Store secrets: [secrets.md](secrets.md)
- Use KV, R2, D1: [kv.md](kv.md), [r2.md](r2.md), [d1.md](d1.md)
- Send and consume messages: [queues.md](queues.md)
- Run code on a schedule: [cron-triggers.md](cron-triggers.md)
- Wait for a person's approval: [approvals.md](approvals.md)
- Serve a webhook or OAuth callback to the internet:
  [public-ingress.md](public-ingress.md)
- Use your MCP server from claude.ai or ChatGPT (OAuth via tsidp):
  [mcp-public.md](mcp-public.md)
- Give a workload a stable name, or replicas: [services.md](services.md)
- Run one job at a time (locks, leader election): [locks.md](locks.md)
- Run a container or code sandbox: [containers.md](containers.md)
- Run a Node.js app: [node-tier.md](node-tier.md)
- Run a Python Worker: [python.md](python.md)
- Read logs, metrics and traces: [observability.md](observability.md)
- Use the admin console: [ADMIN-UI.md](ADMIN-UI.md)
- Let a team manage only its own tenant: [teams.md](teams.md)
- Upgrade tailworks: [upgrading.md](upgrading.md)
- Add hosts, place workloads, size a host: [hosts.md](hosts.md)
- Move mgmt to Postgres, run mgmt replicas: [postgres.md](postgres.md)
- Run on Headscale: [headscale.md](headscale.md)

## Look up: reference

- CLI: [cli.md](cli.md) has every command, subcommand and flag (generated
  from `--help`) and the environment variables `tw` reads; `tw help`
  groups the commands, `tw <command> --help` shows one.
- `tailworks.jsonc`, every key: [tailworks-jsonc.md](tailworks-jsonc.md)
- What works from Cloudflare Workers: [COMPAT.md](COMPAT.md)
- Error codes and their fixes: [errors.md](errors.md)
- `--json` output: [cli-json.md](cli-json.md)
- The management API: [contracts/mgmt-api.md](contracts/mgmt-api.md#operator-api)
- The host agent's own API: [contracts/hostd-api.md](contracts/hostd-api.md)
- Every limit, and the ones that don't exist: [limits.md](limits.md)
- What changed in each release: [CHANGELOG.md](../CHANGELOG.md)
- Versions and release notes: [RELEASING.md](RELEASING.md#versions),
  [compatibility dates](upgrading.md#versions-compatibility-dates-and-the-changelog) and
  the [GitHub releases](https://github.com/rosskukulinski/tailworks/releases).

## Understand: how it works

- Where tailworks fits next to celld, workerd on its own, OpenOrange, Rivet,
  Cloudflare Mesh and tsnet, and when not to use it:
  [alternatives.md](alternatives.md)
- Per-object egress: [egress-modes.md](egress-modes.md)
- How a new Durable Object gets made: [create-admission.md](create-admission.md)
- The design: [RFC](design/RFC.md) and the [ADRs](design/adr/README.md)
