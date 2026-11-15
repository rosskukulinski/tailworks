# twd docs map

Every twd doc, by who you are and by what you need. The same files ship
in the CLI: `twd docs ls`, `twd docs search <words>`,
`twd docs show <doc>[#<anchor>]`.

## Start here by role

- **You write Cloudflare Workers.** Run the app locally first, with no
  tailnet: [Try it without a tailnet](INSTALL.md#try-it-without-a-tailnet).
  Then the [quick start](../README.md#quick-start-deploy-your-first-durable-object).
  Bringing a wrangler project? Read
  [What's different from Cloudflare, in short](wrangler.md#whats-different-from-cloudflare-in-short),
  then [COMPAT.md](COMPAT.md) for each surface.
- **You run the machines.** [INSTALL.md](INSTALL.md): [macOS](INSTALL.md#macos)
  or [Linux](INSTALL.md#linux), then [upgrading.md](upgrading.md),
  [back up and restore](INSTALL.md#back-up-and-restore) and
  [troubleshooting](INSTALL.md#troubleshooting).
- **You review security.** How callers are identified:
  [COMPAT.md#identity](COMPAT.md#identity). How isolated an app is: the
  "Isolation, honestly" note at the top of [INSTALL.md](INSTALL.md) and
  [containers.md](containers.md) (the microVM tier). The attacker's view:
  [THREAT-MODEL.md](../security/THREAT-MODEL.md) and
  [RED-TEAM.md](../security/RED-TEAM.md). The design:
  [RFC](design/RFC.md) and the [ADRs](design/adr/README.md).
- **You are a coding agent.** [agents.md](agents.md), then
  `twd agent setup`. Parse `--json` with [cli-json.md](cli-json.md); look
  up error codes in [errors.md](errors.md). The index for agents is
  [llms.txt](../llms.txt).

## Learn: walk-throughs

- [Quick start](../README.md#quick-start-deploy-your-first-durable-object):
  install, start twd, deploy a Durable Object, call it.
- [Try it without a tailnet](INSTALL.md#try-it-without-a-tailnet): `twd dev`.
- Templates: `twd init <dir> --template <name>`. `hello`, `agent`, `mcp`,
  `ai-agent` (a mock model; `src/model.ts` shows how to point it at an LLM
  gateway on your tailnet), `lock`,
  `refund-approval` (a Workflow that waits for a person), `node-workflow`.

## Do: how-to guides

- Bring a wrangler project: [wrangler.md](wrangler.md)
- Store secrets: [secrets.md](secrets.md)
- Use KV, R2, D1: [kv.md](kv.md), [r2.md](r2.md), [d1.md](d1.md)
- Send and consume messages: [queues.md](queues.md)
- Run code on a schedule: [cron-triggers.md](cron-triggers.md)
- Wait for a person's approval: [approvals.md](approvals.md)
- Serve a webhook or OAuth callback to the internet:
  [public-ingress.md](public-ingress.md)
- Give a workload a stable name, or replicas: [services.md](services.md)
- Run one job at a time (locks, leader election): [locks.md](locks.md)
- Run a container or code sandbox: [containers.md](containers.md)
- Run a Node.js app: [node-tier.md](node-tier.md)
- Read logs, metrics and traces: [observability.md](observability.md)
- Use the admin console: [ADMIN-UI.md](ADMIN-UI.md)
- Upgrade twd: [upgrading.md](upgrading.md)
- Run on Headscale: [headscale.md](headscale.md)

## Look up: reference

- CLI: `twd help` lists every command with its flags; `twd <command> --help`
  shows one. There is no separate CLI page yet.
- `twd.jsonc`, every key: [twd-jsonc.md](twd-jsonc.md)
- What works from Cloudflare Workers: [COMPAT.md](COMPAT.md)
- Error codes and their fixes: [errors.md](errors.md)
- `--json` output: [cli-json.md](cli-json.md)
- The management API: [contracts/mgmt-api.md](contracts/mgmt-api.md)
- Limits live on each feature's page:
  [KV](kv.md#limits), [D1](d1.md#limits), [R2](r2.md#limits-and-differences),
  [public routes](public-ingress.md#limits).
- Versions and release notes: [RELEASING.md](RELEASING.md#versions) and
  the [GitHub releases](https://github.com/rosskukulinski/twd/releases).

## Understand: how it works

- Per-object egress: [egress-modes.md](egress-modes.md)
- How a new Durable Object gets made: [create-admission.md](create-admission.md)
- The design: [RFC](design/RFC.md) and the [ADRs](design/adr/README.md)
