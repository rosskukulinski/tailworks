# CLI `--json` output

Every `tw` command takes `--json`, which makes its output machine-readable
and stable. Scripts and coding agents should always use it. The shape of
every command's output is in
[`schema/cli.schema.json`](../schema/cli.schema.json) (JSON Schema). Its
`$defs` are named after the command: `deploy`, `tenant ls`,
`image public add`.

## Results

On success (exit code 0), stdout carries **one JSON document**: the
command's result. Progress text that the human output prints goes to
stderr, or is dropped.

```sh
tw deploy --json | jq -r .endpoint.magicDNS
```

- Objects may gain properties in later versions. The properties the schema
  lists as required, and the types of all listed properties, are stable.
- Empty lists are `[]`, never `null`.
- `tenant ls`, `host ls` and `image public ls` print a JSON array; every
  other command prints an object.
- A few outputs pass through Go structs without JSON names and so use Go
  field names: `status`'s `workloads[]`, `image push`, and `host vmcheck
  --container`. The schema documents them as they are.
- `status` has one `$defs/status` for both places it runs. On the host it
  reports the supervisor, mgmt, the host and its workloads; on a machine
  set up with `tw link` it has `linked`, `whoami`, `hosts` and each
  tenant's `deployments` instead. `stateDir` and `healthy` are always
  there; check for `linked` to tell which set you got.

## Errors

On failure, stdout is empty and stderr's **last line** is one JSON object:

```json
{"error": {"code": "acl_denied", "message": "preflight failed: …", "hint": "add to your tailnet policy (admin console › Access controls):\n  // merge into your policy: …\n  \"tagOwners\": {…", "doc_url": "https://github.com/rosskukulinski/tailworks/blob/master/docs/errors.md#acl_denied"}}
```

| Field | Always | |
|---|---|---|
| `code` | yes | a code from [errors.md](errors.md); act on this, not on the message |
| `message` | yes | one line for a human |
| `hint` | yes | what to do next: the fix lines (joined with newlines), a usage line, or where to read about the code |
| `doc_url` | yes | the code's entry in errors.md |
| `detail` | no | more lines of context (compiler output, the request that failed) |
| `fix` | no | `hint` as a list of lines |
| `trace_id` | no | correlates with `tw audit --trace <id>` |

The exit code is 1 when a command fails (including a bad flag or a missing
argument, code `bad_request`) and 2 for an unknown command. With `--json`,
both are JSON. Key material is redacted from everything tailworks prints.

`tw mcp` with stdin a pipe is not a `--json` command: its stdout is the
MCP stdio protocol (newline-delimited JSON-RPC). A refusal from mgmt
becomes a JSON-RPC error whose `data` is the error object above.
`tw mcp --json` means `tw mcp --config --json` (as does plain `tw mcp` on a
terminal, without `--json`).

`tw doctor` is the one command that prints a result *and* fails: its report
is on stdout, then `doctor_failed` is on stderr if a check failed.

## Identity and versions

Run `tw whoami --json` first. It says who tailworks acts as and what that
identity may do (`$defs/whoami`): `kind` and `actor` (your tailnet login
through `tw link`, or `local` for the operator on the tailworks host), `mgmt`
(where the CLI sends requests), `tenant`, the `github.com/rosskukulinski/tailworks/cap/*` capabilities
in `caps`, and `can`: `admin` (deploy, ls, logs and the rest), `approve`
(`tw approve`) and `mcpTools` (what `/mcp` allows). `approve` and
`mcpTools` are `null` when mgmt is older than the CLI and doesn't report
them. `tenant` is `null` for an identity that isn't an admin, because it
can't list tenants.

`tw version --check --json` adds `latest`, `url`, `updateAvailable`,
`upgrade` (the command that installs the newer release) and `daemons` to
`tw version`'s fields. `daemons` is what the running `tw up` on this
machine reports: `version` and `restartNeeded` (it runs an older binary than
this CLI), or `null` when no `tw up` runs here. A failed lookup is
`update_check_failed`. `--json` never prints the once-a-day update notice
that an interactive tailworks prints on stderr. `tw version --json`'s
`updateCheck` says whether that notice's check is on (a release build
without `CI` or `TW_NO_UPDATE_CHECK` set).

`tw completion <shell> --json` is `{"shell", "script"}`; without `--json`
it prints the script.

## Event streams (NDJSON)

Commands that take a while, or don't end on their own, print one compact JSON
object per line. Every line has `event` (its kind) and `ts` (RFC 3339, UTC,
when tailworks wrote it).

### `tw deploy --json`: progress on stderr

The result is still the one document on stdout. Progress goes to **stderr**,
in this order:

| `event` | Fields |
|---|---|
| `start` | `workload`, `tenant`, `dir` |
| `preflight` | `ok`, `skipped`, `preflight` (what the policy check found) |
| `build` | `bundleBytes`, `runtime` |
| `log` | `line`, `source: "image-build"` (output of `docker buildx`, containers only) |
| `placed` | `deploymentID`, `host`, `placement` |
| `status` | `deploymentID`, `status`, `node` (each time either changes) |
| `ready` | `deploymentID`, `host`, `magicDNS`, `url`, `images` (container classes: each image's pin, as the result's `images`) |
| `public_wait`, `public` | `url` (with `--wait-public`) |

A failure ends the stream with the usual `{"error": …}` line instead.

### `tw rollback --json`: progress on stderr

`tw rollback <workload> [<deployment>] --json` redeploys a stored bundle
and waits like `tw deploy`. Its result (`$defs/rollback`) names the new
`deploymentID`, the deployment it went back to (`rollbackOf`) and that
deployment's history record (`target`). Progress goes to **stderr** as
`$defs/deploy.event` lines: `start` (with `target`, empty for the default),
`preflight`, `placed` (with `rollbackOf`), `status` and `ready`. There is no
`build`: nothing is rebuilt.

`tw policy --json` (`$defs/policy`) is one document: `ok`, `checked`
(false when mgmt can't read the policy), the dry-run `preflight`, the
`callerGrant` tailworks can't check, for an app behind a Tailscale Service
also `serviceCallerGrant` (the grant to its `svc:` name, where callers' app
capabilities go), and `snippet`, everything to add in one block. When a checked part is missing, it is an `acl_denied` (or
`funnel_required`) error instead, and the snippet is in its `fix`.

`tw deployments <workload> --json` (`$defs/deployments`) is one document:
the history newest first, each entry a `$defs/deploymentRecord` with its
`bundleSHA256` and `bundleRetained` (false once mgmt has pruned the bundle,
so it can't be rolled back to), and `bundleRetention`, how many bundles
mgmt keeps per workload.

### Previews: `tw deploy --preview`, `tw previews`, `tw promote`

`tw deploy --preview --json` is a deploy (`$defs/deploy`, the same
events) whose result also has `preview`, the preview's own workload name
(`<workload>--preview-<alias>`); `workload` stays the workload it
previews. `tw previews <workload> --json` (`$defs/previews`) lists the
running previews, newest first, each with its `alias`, `bundleSHA256` and
`deployment`. `tw promote <workload> <preview> --json` (`$defs/promote`)
waits like `tw rollback`: its result names the workload's new
`deploymentID`, the preview (`preview`), the preview deployment whose
bundle it deployed (`promotedFrom`) and whether the preview was undeployed
(`previewStopped`). Its events are `start` (with `preview`), `preflight`,
`placed` (with `promotedFrom`), `status` and `ready`.

### `tw dev --json`: events on stdout

Without `--once`, `tw dev` runs until it is stopped, so its output is a
stream on **stdout**:

| `event` | Fields |
|---|---|
| `ready` | `url`, `serves` (with the MCP route), `workload`, `node`, `as`, `tags`, `timingsMs`; with `--caller`, `callers` (`name`, `url`, `as` or `tags`, `nodeId`, `node`; see [testing.md](testing.md#black-box-tests-tw-dev---caller-and-startdev)) |
| `inspector` | `addr`, `ws` (with `--inspector-port`: where a debugger attaches; [dev-debugging.md](dev-debugging.md)) |
| `console` | `url` (the read-only data console: KV keys and D1 rows of the run; not with `--console-port -1`; [dev-debugging.md](dev-debugging.md#look-at-the-data-the-dev-console)) |
| `reload` | `workload`, `node`, `ms` (after a file change) |
| `request` | `record`: one request record, as in `tw logs --json` (`$defs/logs.record`), with its console lines and exceptions |
| `error` | `error` (a rebuild failed; the last good version keeps running) |
| `stopping` | (Ctrl-C or SIGTERM) |

`tw dev --once --json` is a single result document instead (`$defs/dev`).

### `tw logs --json`: one line per record or log line on stdout

`tw logs <workload> --json` prints the workload's request records, one
per line (`$defs/logs.record`: `time`, `kind`, `method`, `path`, `status`,
`durationMs`, `outcome`, `object`, `caller`, `traceId`, `logs`,
`exceptions`, …). Raw output (`--console`, `--component`) is one event per
line (`$defs/logs.line`):

```json
{"event": "log", "ts": "…", "workload": "hello-mcp", "line": "…"}
```

`workload` is set for `tw logs <workload> --console` (and for a bare
`tw logs`, which picks the app in the current directory, else the only
workload), `component` only for `--component mgmt|hostd|supervisor`. From a machine linked with `tw link`,
replica lines also carry the `host` they came from. With `-f`, lines keep
coming until the command is stopped.

## Node budget (`nodes`)

`tw policy`, `tw deploy` and `tw ls` carry the tenant's tailnet nodes in
`nodes` (schema `$defs/nodeBudget`;
[limits.md](limits.md#tailnet-node-budget) has the counting rule):

```json
{"nodes": {
  "app": {"workload": 1, "public": 0, "fixed": 1, "perObjectClasses": ["Room"], "sharedClasses": ["Counter"]},
  "tenant": {"total": 14, "workloads": 2, "objects": 11, "pool": 1, "public": 0, "maxObjects": 1000},
  "capacity": {"hosts": 1, "capacityNodes": 500, "liveNodes": 14},
  "warnings": []
}}
```

- `app` is what the app uses, from its `tailworks.jsonc`: `fixed` nodes at
  deploy, plus one per object of each class in `perObjectClasses`.
  `tw ls` has no `app`.
- `tenant` is the live count now (for `tw deploy`, before the deploy).
  It is absent if mgmt couldn't be asked; `capacity` is absent unless the
  caller is an admin.
- `warnings` is always present. It has a line when `tenant.total` is
  within 80% of `maxObjects` or of the hosts' node capacity. A warning
  never fails the command.

`tw doctor` reports the same count in its `node_budget` check, with
`status: "warn"` near a limit.

## Listings (`tw ls`)

`tw ls --json` lists the live objects (`pending`, `minted`, `assigned`,
`ready`) and the deployments that aren't stopped; `--all` adds killed
objects and stopped deployments. `tw ls <workload>` keeps that workload,
its previews and its replicas (`namespaces[].previewOf`, `replicaOf`).
At most 5000 objects are listed (`--limit N`, `0` for no ceiling), and
`truncated` is `true` when more matched. `nodes.tenant.total` is always
the tenant's whole live count.

## Confirmations

`--json` never prompts. Commands that ask before acting (`tw up`,
`tw agent setup`, `tw agent remove`) fail without `--yes` when there's no
terminal or `--json` is given: `tw up` with `bad_request`, `tw agent` with
`confirmation_required`. See
[Using tailworks with a coding agent](agents.md#safety-rules-the-skills-give-agents).

## Tests

`cmd/tw:TestCLIJSONContract` runs every command and subcommand with
`--json` and checks results against the schema and errors against this page.
It fails if a command has no schema, or the schema has one that's gone.
`cmd/tw:TestAgentEval` checks the successful outputs against a running
tailworks, including the deploy events. `cmd/tw:TestDevJSONEvents` checks the
`tw dev` stream.
