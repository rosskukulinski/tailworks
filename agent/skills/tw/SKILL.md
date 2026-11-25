---
name: tw
description: Use when the user wants to build, run, deploy, inspect, debug or stop a Worker, Durable Object, Agent or MCP server on tailworks (a self-hosted, tailnet-native Workers runtime driven by the `tw` CLI). Start here; it maps each task to the right tailworks skill and command.
---

# tailworks: Workers and Durable Objects on a tailnet

tailworks runs Cloudflare-style Workers and Durable Objects on the user's own
machines, on stock workerd. Every workload, and every Durable Object
instance, is its own node on the user's Tailscale tailnet. Callers are
identified by their WireGuard identity (`getCaller(req)` from
`@tailworks/tailnet`), never by tokens or API keys. Tailnet policy (tags,
`tagOwners`, `grants`) decides who can call what.

You drive tailworks only through the `tw` CLI.

## Before anything else

```sh
tw version --json               # is tw installed? which version?
tw status --json                # is a tw control plane up (or linked)?
tw whoami --json                # who am I to it, and may I deploy, approve, use MCP tools?
```

- `tw whoami` shows the identity tailworks acts as (your tailnet login through
  `tw link`, or the local operator on the tailworks host), the tenant, and the
  `can` flags: `admin` (needed for deploy, ls, logs and the rest),
  `approve` and `mcpTools`. Don't try a command the identity can't run;
  tell the user which grant it needs.

- If `tw` is not found, point the user at the install steps in the tailworks
  README; don't install it yourself.
- If `tw status` fails with `not_up`, ask the user to start tailworks (`tw up`,
  run once by a tailnet admin) or link this machine to one
  (`tw link tw-mgmt.<tailnet>.ts.net`). Both need the user's tailnet
  credentials, so the user runs them, not you.
- `tw doctor --json` checks this machine and prints a fix for each failing
  check.

## Which skill for which task

| Task | Skill | Main commands |
|---|---|---|
| New Worker, Durable Object, Agent or MCP server | `tw-scaffold` | `tw init` |
| Run it locally, no tailnet | `tw-dev` | `tw dev` |
| Deploy it to the tailnet | `tw-deploy` | `tw deploy`, `tw ls`, `tw status` |
| Logs, a failing deploy, an error code | `tw-debug` | `tw logs`, `tw doctor`, `tw audit`, `tw docs` |
| Let something call it (or let it call something) | `tw-policy` | tailnet policy `tagOwners` and `grants` |
| API keys and other secrets | `tw-secrets` | `tw secret` |
| Cron triggers | `tw-cron` | `triggers.crons`, `tw dev --test-scheduled` |
| Public URLs (webhooks, OAuth callbacks) | `tw-public` | `tailworks.jsonc` `public`, `tw expose` |
| Stop, kill or remove something | `tw-teardown` | `tw undeploy`, `tw kill` |

If your agent can't load skills by name, run `tw agent show <skill>` to
print one, and `tw agent ls` to list them.

## tailworks' MCP server

tailworks also serves its operations as MCP tools (`whoami`, `status`, `ls`,
`logs`, `audit`, and when granted `deploy`, `kill`, `undeploy`). If you
have them, prefer them for inspecting and operating: they are typed,
authorized per tool by the user's tailnet policy, and audited. A
`cap_missing` error means the user hasn't granted that tool; show them its
`snippet` instead of working around it with the CLI. To set it up, the
user runs `tw mcp --config`: it prints the command or config for each
agent.

## Rules for agents

1. **Use `--json` and parse it.** Every command takes `--json`. A result is
   one JSON document on stdout. An error is one line on stderr:
   `{"error": {"code", "message", "hint", "doc_url", ...}}`. `tw deploy
   --json` also writes progress events on stderr, one JSON object per line
   (`{"event": "build", ...}`). `tw dev --json` and `tw logs -f --json`
   stream one JSON object per line on stdout. The schemas are in
   `schema/cli.schema.json` in the tailworks repo.
2. **Act on the error code**, not the message text. `hint` says what to do
   next. `tw docs show errors.md#<code>` prints the full entry offline.
3. **Check flags before you guess them.** `tw <command> --help` lists every
   flag. Don't invent flags; tailworks refuses unknown ones with `bad_request`.
4. **Ask before anything destructive.** None of these has an undo. Run one
   only after the user has said yes to that specific action.
   - `tw kill`, `tw undeploy`, `tw down`, `tw secret rm` and
     `tw kv key rm` act immediately.
   - `tw d1 rm`, `tw kv namespace rm`, `tw queues rm`,
     `tw queues purge`, `tw r2 bucket rm`, `tw tenant rm` and
     `tw host rm` delete data or a tenant's resources. Without a terminal
     they refuse with `confirmation_required`. Tell the user what will be
     lost, and add `--yes` only after they agree.
   - Never pass `--yes` to `tw up` or `tw agent setup` on the user's
     behalf; those prompts exist so the user sees what will change.
5. **Never handle credentials.** Don't write API keys, OAuth client
   secrets or auth keys into files, commands or `tailworks.jsonc`. Secrets go
   in with `tw secret put <NAME>`, which reads the value from stdin; let
   the user type or pipe it (see `tw-secrets`).
6. **The tailnet policy belongs to the user.** When tailworks prints a policy
   snippet (`tagOwners`, `grants`), show it to the user and ask them to add
   it in the Tailscale admin console. Tailworks writes the policy only
   after the user answers y to the lines it shows (on a terminal), or with
   `--apply-policy --yes`. With `--json` nothing asks and nothing is
   written. Pass `--apply-policy --yes` only after the user agreed to the
   lines you showed them, and don't edit the policy yourself.

## More

- `tw help` lists every command by group; `tw help --all` prints every
  usage line; `tw <command> --help` shows a command's flags.
- `tw docs search <words>` searches tailworks' docs offline;
  `tw docs show <doc>[#<anchor>]` prints a doc or one section.
- What works and what doesn't, compared with Cloudflare Workers:
  `tw docs show COMPAT.md`.
