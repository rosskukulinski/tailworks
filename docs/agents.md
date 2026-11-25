# Using tailworks with a coding agent

tailworks ships **skills** that teach coding agents (Claude Code, Codex, Cursor,
Gemini CLI, OpenCode and others) how to drive the `tw` CLI. With them, you
can ask your agent to "build me an MCP server and run it on tw" and it
knows how to scaffold it, run it locally, deploy it, read its logs, debug a
failing deploy and stop it, without guessing at commands.

The skills are Markdown files in [`agent/skills/`](../agent/skills/), built
into the `tw` binary, so they always match the CLI you have installed.

## Install the skills

```sh
tw agent setup --dry-run      # what it would write, and where
tw agent setup                # asks before writing anything
```

`tw agent setup` looks for the coding agents on this machine (their config
directories in your home, and their binaries on `PATH`), prints every file it
would create or change, and asks before it writes. Run it again after
upgrading tailworks to update the skills; when nothing changed it does nothing.
`tw agent remove` deletes exactly what setup wrote.

| Agent | `--scope user` (default) | `--scope project` |
|---|---|---|
| Claude Code | `~/.claude/skills/<skill>/SKILL.md` | `.claude/skills/<skill>/SKILL.md` |
| Codex | a block in `~/.codex/AGENTS.md` | a block in `AGENTS.md` |
| OpenCode | a block in `~/.config/opencode/AGENTS.md` | a block in `AGENTS.md` |
| Gemini CLI | a block in `~/.gemini/GEMINI.md` | a block in `GEMINI.md` |
| Cursor | not supported (Cursor keeps user rules in its settings) | `.cursor/rules/<skill>.mdc` |

- Claude Code and Cursor get one file per skill. The `AGENTS.md` and
  `GEMINI.md` agents get one block: the core `tw` skill in full, and a list
  of the others, which the agent prints with `tw agent show <skill>` when it
  needs one.
- The block is delimited by `<!-- BEGIN tw agent skills -->` and
  `<!-- END tw agent skills -->`. Everything outside it is yours; setup and
  remove never touch it.
- Every file and block tailworks writes carries a marker comment. Setup won't
  overwrite a file without it (it reports `skip`), and remove only deletes
  files and blocks with it.
- `--agent claude,cursor` picks agents explicitly, for example to set up a
  project for an agent that isn't installed on this machine.
- `--scope project --dir <dir>` writes into a project (default: the current
  directory), so the skills are committed with it. The files contain no
  credentials.
- `--json` prints the plan (and the outcome) as JSON. Without a terminal,
  setup and remove need `--yes`; they never write without it.

## Without setup

- `tw agent ls` lists the skills, and which agents it found.
- `tw agent show <skill>` prints one; paste it, or point your agent at it.
- [`llms.txt`](../llms.txt) at the repo root indexes these docs for agents
  that read the repo.
- `tw docs search <words>` and `tw docs show <doc>[#<anchor>]` (for
  example `tw docs show errors.md#acl_denied`) read the docs offline.

## The skills

| Skill | For |
|---|---|
| `tw` | the entry point: what tailworks is, the rules, which skill for which task |
| `tw-scaffold` | `tw init` templates: Worker, Durable Object, Agent, MCP server, AI agent, Node workflow |
| `tw-dev` | `tw dev`, the `--once` smoke test, `.dev.vars` |
| `tw-deploy` | `tw deploy`, preflight failures, `tw ls`, `tw status` |
| `tw-debug` | `tw logs`, error codes, `tw doctor`, `tw audit` |
| `tw-policy` | tailnet policy: `tagOwners`, grants, MCP tool capabilities |
| `tw-secrets` | `tw secret`, without the agent ever seeing a value |
| `tw-cron` | cron triggers and testing them locally |
| `tw-public` | public routes over Funnel, `tw expose` |
| `tw-teardown` | `tw undeploy`, `tw kill`, and which commands ask first |

## Safety rules the skills give agents

- **Credentials stay with you.** The skills tell agents never to write API
  keys, OAuth client secrets or auth keys into files, commands or config.
  `tw secret put` reads a value from stdin; the skill asks the agent to let
  you type it. `tw up` and `tw link` need your tailnet credentials, so the
  agent asks you to run them.
- **Policy stays with you.** When a deploy needs a `tagOwners` line or a
  grant, tailworks prints it; the agent shows it to you and you add it in the
  Tailscale admin console. Tailworks writes the policy only after you
  answer y to the lines it shows on a terminal, or with `--apply-policy
  --yes`. An agent runs tw with `--json`, which never asks or writes the
  policy; the skills tell it to pass `--apply-policy --yes` only after you
  agreed to the lines it showed you.
- **The MCP server enforces it.** Over MCP, `kill`, `undeploy` and
  `deploy` exist for an agent only if a `github.com/rosskukulinski/tailworks/cap/mcp` grant (or the
  operator's `tw mcp --allow`) names them. See
  [tailworks' MCP server](#tws-mcp-server).
- **Destructive commands need your go-ahead.** `tw kill`, `tw undeploy`,
  `tw down` and `tw secret rm` act immediately and have no undo. The
  commands that delete data or a tenant's resources (`tw d1 rm`,
  `tw kv namespace rm`, `tw queues rm`, `tw queues purge`,
  `tw r2 bucket rm`, `tw tenant rm`, `tw host rm`) ask first, and refuse
  with `confirmation_required` when there is no terminal. The skills tell
  agents to run any of them, or add `--yes`, only after you've agreed to that
  specific action.
- **Confirmations aren't bypassed for agents.** `tw up`, `tw agent setup`
  and `tw agent remove` ask before acting. Without a terminal (which is how
  most agents run commands) they refuse with `bad_request` (`tw up`) or
  `confirmation_required` (`tw agent`) instead of proceeding. `--yes` skips
  the question; the skills tell agents not to add it on your behalf, and to
  show you `--dry-run` output instead.

## Output agents can parse

Every command takes `--json`. Results are one JSON document on stdout;
errors are `{"error": {"code", "message", "hint", "doc_url"}}` on stderr,
with codes from [errors.md](errors.md). `tw deploy` streams progress events,
and `tw dev` and `tw logs -f` stream one JSON object per line. Details and
the schema: [cli-json.md](cli-json.md).

## How the skills are kept honest

- `cmd/tw:TestSkillCommandsExist` runs every `tw …` command in every skill
  as `tw … --help`, which parses each flag. A skill that names a command or
  flag that no longer exists fails `make test`.
- `cmd/tw:TestAgentEval` is a scripted agent with no LLM. It replays the
  skills' marked command blocks (`<!-- tw-eval: <step> -->`) in order
  (`tw init`, `tw dev --once`, `tw deploy`, `tw ls`, `tw logs`,
  `tw undeploy`) against a real tailworks on the hermetic test harness. That
  includes the first deploy failing with `acl_denied` and succeeding once
  the `tagOwners` line from the error's hint is added. Every output is checked
  against the schema.
- `cmd/tw:TestAgentSetupSnapshot` snapshots `tw agent setup --dry-run` for
  a home directory with five agents' config dirs, and checks that setup is
  idempotent and remove restores your files byte for byte.

## tailworks' MCP server

As well as the CLI, an agent can operate tailworks through **MCP**. mgmt serves
an MCP server at `/mcp`, next to its `/v1` API, behind the same tailnet
identity check. The design is in
[ADR-035](design/adr/035-mgmt-mcp.md).

| Tool | What it does | Who gets it |
|---|---|---|
| `whoami` | who tailworks thinks you are, your tools, the grant for the rest | everyone admitted |
| `status`, `ls`, `logs`, `audit` | like the CLI commands (`logs` gives request records with `tw logs`' filters, `console: true` the raw output; `audit` takes `human`) | tailworks admins and any `github.com/rosskukulinski/tailworks/cap/mcp` holder |
| `deploy` | deploy a bundle, or redeploy (`replace: true`: re-place) the current one | a `github.com/rosskukulinski/tailworks/cap/mcp` grant naming it |
| `kill`, `undeploy` | no undo; a `reason` is required and audited; `kill` takes the object's sub-agents too, like `tw kill` (`cascade: false`: only the object) | a `github.com/rosskukulinski/tailworks/cap/mcp` grant naming it |

Register it with your agent through the CLI's stdio bridge. It works on
the tailworks host and on any `tw link`ed machine, and needs no credentials:

```sh
tw mcp --config                         # the command or config for each agent, and your tools
claude mcp add -s user tw -- tw mcp     # Claude Code
codex mcp add tw -- tw mcp              # Codex
gemini mcp add -s user tw tw mcp        # Gemini CLI
```

Cursor and OpenCode take JSON in their config files (`~/.cursor/mcp.json`,
`~/.config/opencode/opencode.json`); `tw mcp --config` prints it, and for
any other agent the server is command `"tw"`, args `["mcp"]`. Agents
installed on this machine are listed first.

Run on a terminal, `tw mcp` prints the same help as `tw mcp --config`
rather than wait for JSON-RPC: your agent runs it with stdin a pipe. To
talk to the bridge by hand, pipe into it:
`printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | tw mcp`.

An agent on a tailnet machine without the tailworks CLI can use the URL
`tw mcp --config` prints (`http://tw-mgmt.<tailnet>.ts.net/mcp`).

**Who may do what is your tailnet policy.** A grant to `tag:tw-mgmt` with
the app capability `github.com/rosskukulinski/tailworks/cap/mcp` admits a user, group or tagged machine
to `/mcp` (and only `/mcp`: it isn't a tailworks admin) with the read tools.
Destructive tools need the grant to name them, even for admins:

```jsonc
// a coding agent's machine: read-only
{"src": ["tag:agent"], "dst": ["tag:tw-mgmt"], "ip": ["tcp:443", "tcp:80"], "app": {"github.com/rosskukulinski/tailworks/cap/mcp": [{}]}},
// this user's agents may also kill and undeploy
{"src": ["alice@example.com"], "dst": ["tag:tw-mgmt"], "ip": ["tcp:443", "tcp:80"], "app": {"github.com/rosskukulinski/tailworks/cap/mcp": [{"tools": ["kill", "undeploy"]}]}},
```

A tool you don't have answers `cap_missing`, with the grant to add as its
`snippet`. On the tailworks host itself the bridge runs as the operator: it is
read-only unless you enable more when you register it, for example
`claude mcp add -s user tw -- tw mcp --allow kill,undeploy`.

`deploy` over MCP needs a bundle, which only `tw deploy` builds, so in
practice an MCP agent redeploys or re-places what's running; new code
still goes through the CLI.
