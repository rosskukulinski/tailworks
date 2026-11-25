Last update: 2026-11-15 PT. Added twd's own MCP server (`/mcp` on mgmt, `twd mcp` stdio bridge), ADR-035.

Earlier: agent-first developer experience (issue #25): skills, `twd agent`, stable `--json`, `twd docs`, `llms.txt`.

# Agent-first developer experience (issue #25)

Branch `agent-first-dx-issue-25` (worktree `ancient-alpacka`).

## What's built

- **Skills** (`agent/skills/<name>/SKILL.md`, embedded in the binary by
  package `agent`): `twd` (entry point and rules), `twd-scaffold`,
  `twd-dev`, `twd-deploy`, `twd-debug`, `twd-policy`, `twd-secrets`,
  `twd-cron`, `twd-public`, `twd-teardown`. Claude Code skill format
  (front matter `name`, `description`) is the source; the other formats
  are rendered from it.
- **`twd agent setup | remove | ls | show`** (`cmd/twd/agent.go`, logic in
  `agent/agent.go`). Detects Claude Code, Codex, Cursor, Gemini CLI and
  OpenCode by config dir (`~/.claude`, `~/.codex`, `~/.cursor`, `~/.gemini`,
  `~/.config/opencode`) or binary on `PATH`. Writes Claude skill dirs,
  Cursor `.mdc` rules (project scope only), or one managed block in
  `AGENTS.md` / `GEMINI.md` (core skill in full plus an index; the agent runs
  `twd agent show <skill>` for the rest). `--scope user|project`, `--dir`,
  `--agent`, `--dry-run`, `--yes`. Prints every change and asks; without a
  terminal or with `--json` it needs `--yes` (`confirmation_required`).
  Idempotent; a marker comment on everything it writes, so it never
  overwrites (`skip`) or removes a file it didn't write, and removes stale
  skills a newer twd no longer ships. Remove restores files byte for byte.
- **`--json` contract** (`cmd/twd/agentjson.go`, `docs/cli-json.md`,
  `schema/cli.schema.json`):
  - every error is `{"error": {code, message, hint, doc_url, …}}` on
    stderr; `hint` is always set; usage errors and unknown commands are JSON
    too (`--json` is detected before the command parses);
  - `twd deploy --json`: NDJSON progress events on stderr (`start`,
    `preflight`, `build`, `log`, `placed`, `status`, `ready`, `public_wait`,
    `public`); the result document on stdout is unchanged, so existing
    `MustJSON` callers still work;
  - `twd dev --json` (without `--once`): NDJSON on stdout (`ready`,
    `reload`, `error`, `stopping`);
  - `twd logs --json` (and `-f`): one `{"event": "log", "line", "workload"
    | "component", "host"?}` per line; it used to ignore `--json`;
  - `--json` never prompts: `twd up --json` without `--yes` fails like a
    non-terminal does (it used to prompt on a TTY);
  - `schema/cli.schema.json`: a `$defs` entry per command and subcommand,
    the three streams, and the error; checked with the existing JSON Schema
    subset checker in package `schema` (now parameterised by root).
- **`twd docs search | show | ls`** (`docs/docs.go` embeds `docs/**/*.md`;
  `cmd/twd/docs.go`): section search, `twd docs show errors.md#acl_denied`.
- **`llms.txt`** at the repo root.
- **Docs**: README "Using twd with a coding agent", `docs/agents.md`,
  `docs/cli-json.md`, three COMPAT.md rows (new "Tooling" area: `cli-json`,
  `agent-skills`, `docs-for-agents`) with README rows.

## twd's MCP server (issue item 4)

Design: `docs/design/adr/035-mgmt-mcp.md` (ADR-035, Proposed).

- `mgmt/mcp.go`: `POST /mcp` on mgmt's listeners, through the same WhoIs
  gate as `/v1`; MCP Streamable HTTP, stateless (one JSON-RPC message per
  POST, `application/json`; notifications `202`; `GET` `405`). Tools run the
  `/v1` handlers in process, so errors, validation and audit are the
  API's: `whoami`, `status`, `ls`, `logs` (request records, or `console`),
  `audit`, `deploy` (a bundle, or redeploy/re-place the current one),
  `kill`, `undeploy` (`reason` required).
- Authorization: new app capability `twd.dev/cap/mcp` with
  `[{"tools": [...]}]`. Holding it (or `twd.dev/cap/admin`) gives the read
  tools; `deploy`, `kill`, `undeploy` need a value naming them, **admins
  included**. A caller with only the MCP cap is principal kind `mcp`,
  admitted to `/mcp` and nothing else (`/v1`, `/ui`, `/v2` refused). Not
  granted → tool error `cap_missing` with the grant as `snippet`. Local
  operator (loopback, unix socket): read tools, or exactly the
  `Twd-MCP-Tools` header's list (the bridge's `--allow`); for tailnet
  callers the header only narrows. Hosts and browsers refused.
- `WhoIsInfo` / `Principal` carry `CapValues` (filled for the MCP cap in
  `internal/mgmtd`).
- `api/openapi.yaml`: `/mcp` documented under tag `mcp`, excluded from
  codegen like the registry (`exclude-tags`, `excludedTags`); `make api`
  regenerated only `docs/contracts/mgmt-api.md`.
- CLI `twd mcp [--allow deploy,kill,undeploy]`: stdio MCP server relaying
  to `/mcp` over the CLI's transport (no credentials); doesn't probe
  `/v1/whoami` on linked machines (an MCP-only user can't). `twd mcp
  --config [--json]`: registration commands, URL, your tools and the grant
  for the rest (`$defs/mcp`).
- Tests: `mgmt:TestMCPServer` (the tool matrix for admin, MCP-only,
  granted, `*`, header narrowing, local with and without the header, host,
  no cap; MCP-only refused on `/v1`, `/ui`, `/v2`; tools reach the `/v1`
  handlers' answers; JSON-RPC edges; browsers refused).
  `cmd/twd:TestAgentEval` now also runs the bridge as the operator (read
  tools, `kill` refused without `--allow`, `--allow deploy` redeploys
  `hello-mcp` and the deployment is attributed `… (mcp)`), and a separate
  tsnet node holding only a read-only MCP grant calls `/mcp` over the
  tailnet (read tools, `kill` → `cap_missing` with the grant, `/v1/status`
  → 403).

## After merging observability (#30)

`twd logs <workload>` now prints structured request records by default;
with `--json` they are JSON Lines of `obs.Record` (`$defs/logs.record`).
The `{"event": "log", "line"}` stream (`$defs/logs.line`) applies to raw
output: `--console`, `--component`, and a host's notes in a remote record
stream (which used to be printed as plain text in the middle of the JSON
Lines). `twd telemetry [show|set|rm]` got `$defs/telemetry`;
`TestCLIJSONContract` flagged it as a command without a schema. The
`twd-debug` skill teaches the record filters (`--errors`, `--status`,
`--trace`, ...).

## Bugs found by the `--json` audit (fixed)

- `twd host rm <id>` failed with `internal: unexpected end of JSON input`
  whenever the host had nothing to drain: mgmt answers `204 No Content` and
  the shared mgmt client decoded the empty body. Fixed in
  `hostd/mgmtapi/client.go` (an empty 2xx body leaves `out` zero).
- `twd image public ls --json` printed `null` with no images (`listAll`
  returned a nil slice); every paged list is now `[]` when empty.
- `twd link --unlink --json` printed `"tenants": null`.
- `twd logs --json` printed plain text.
- Five CLI error codes were not in `docs/errors.md`: `host_failed`,
  `image_build_failed`, `mgmt_failed`, `public_unreachable` (existing) and
  `confirmation_required` (new). `TestCLIJSONContract` and the eval check
  every code they see against the catalog.
- `make test-go` didn't run `./schema/...` (twd.jsonc schema tests); it now
  runs `./agent/... ./docs/... ./schema/...` too.

## Tests

| Test | What it proves |
|---|---|
| `cmd/twd:TestSkillCommandsExist` | every `twd …` in every skill (code blocks and inline code; 121 commands) runs as `twd … --help`, which parses each flag: a stale command or flag fails `make test`. Negative cases prove the check catches them |
| `cmd/twd:TestSkillsWellFormed` | front matter valid for every agent, cross-skill references and `twd docs show` refs resolve, eval steps present and placeholder-free |
| `cmd/twd:TestAgentEval` | the scripted agent loop: replays the skills' `<!-- twd-eval -->` blocks (scaffold → dev → deploy → inspect → logs → teardown) on the hermetic harness (testcontrol, fake Tailscale API, `twd up` single process). First deploy fails `acl_denied` with the tagOwners line in `hint`; the "user" adds it; the agent retries as `twd-deploy` says. Every other command (status, version, doctor, tenant ls/set, host add/ls/rm, secret put/ls/rm, expose, audit, kill, image public ls, docs, agent) runs once and its output is checked against the schema |
| `cmd/twd:TestCLIJSONContract` | every command and subcommand in `twd help` (41 entries) runs with `--json` against an empty state dir; results match the schema, errors match the contract with catalogued codes; fails if a command has no schema or the schema has a command that's gone |
| `cmd/twd:TestDevJSONEvents` | `twd dev --json`: ready → reload after an edit → stopping on SIGINT |
| `cmd/twd:TestAgentSetupSnapshot` | golden `cmd/twd/testdata/agent-setup.golden`: dry run for a home with five agents' dirs (user and project scope), writes nothing, idempotent, remove restores the user's files |
| `agent:TestPlanStaleAndForeign`, `agent:TestPlanBlock`, `agent:TestCommandsParse` | stale removal, user files skipped, block replaced in place, broken block refused |
| `docs:TestLLMsTxt`, `docs:TestShowAndSearch` | llms.txt links resolve and list every doc; search and section show |
| `cmd/twd:TestRemoteLogs` (extended), `TestLogLinesSplitHost`, `TestJSONErrorHint` | logs NDJSON from a linked machine, replica host prefix, hint/doc_url |

Timings on the dev Mac (arm64): `TestAgentEval` ~25 s, `TestCLIJSONContract`
~8 s, `TestDevJSONEvents` ~6 s, `TestAgentSetupSnapshot` ~5 s.

## Test runs

- `make lint test` with the MCP server: PASS, no failures (`cmd/twd`
  168 s, `mgmt` 136 s).

- `make lint test` after the first master merge: PASS (`cmd/twd` 184 s).
- `make lint test` after merging observability (#30): `cmd/twd`, `agent`,
  `docs`, `schema` and all other packages pass; `hostd:TestContainerPolyfill/SurvivesRedeploy`
  failed once under the full parallel run (exec output correct, the
  container's generation changed), then passed 3/3 alone
  (`go test -race -count=1 -run TestContainerPolyfill ./hostd/`). This branch
  doesn't touch that path; recorded as a load-dependent flake.

## Decisions

- **deploy's events go to stderr, not stdout.** The issue asks for NDJSON
  from long-running commands. `twd deploy --json` has a result that 20+
  e2e callers (and users' scripts) decode as one document, so stdout keeps
  that and the events go to stderr, where `--json` errors already were.
  stderr is then itself NDJSON, ending in the error line on failure. `dev`
  and `logs -f` don't end on their own, so they stream on stdout.
- **Destructive commands keep their (lack of) confirmations.** `kill`,
  `undeploy`, `down --purge`, `tenant rm`, `host rm` and `secret rm` have
  never prompted; adding prompts would break every script. The skills tell
  agents to get the user's explicit OK first, and never to add `--yes` for
  them. Documented in `docs/agents.md` and `twd-teardown`.
- **AGENTS.md agents get a block, not every skill.** Concatenating ten skills
  into a user's `AGENTS.md` is a lot of always-on context; the core skill
  plus an index and `twd agent show` keeps it short and always current.
- **Go field names left as is** in `status.workloads[]`, `image push` and
  `host vmcheck --container`: renaming them would change hostd's wire
  format and break decoders. The schema documents them; candidates for a
  later cleanup.

## Gaps and next steps

- MCP server: no tools for secrets, tenants, hosts, expose or telemetry;
  `deploy` can't build a bundle; no per-tool limits like `@twd/mcp`'s.
- Success-path output of `link`, `tenant add/rm`, `host setup/vmcheck`,
  `image public add/rm` and `image push` is schema-documented from the code
  but only their error path runs hermetically (`TestCLIJSONContract`); the
  existing `TestCLILink`, `TestCLIMultiTenant` and image tests don't check
  it against the schema yet.
- `twd agent setup` doesn't detect agents installed only as editor
  extensions (e.g. Claude Code inside an IDE without `~/.claude`); `--agent`
  covers it.
