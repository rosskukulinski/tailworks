# CLI `--json` output

Every `twd` command takes `--json`, which makes its output machine-readable
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
twd deploy --json | jq -r .endpoint.magicDNS
```

- Objects may gain properties in later versions. The properties the schema
  lists as required, and the types of all listed properties, are stable.
- Empty lists are `[]`, never `null`.
- `tenant ls`, `host ls` and `image public ls` print a JSON array; every
  other command prints an object.
- A few outputs pass through Go structs without JSON names and so use Go
  field names: `status`'s `workloads[]`, `image push`, and `host vmcheck
  --container`. The schema documents them as they are.

## Errors

On failure, stdout is empty and stderr's **last line** is one JSON object:

```json
{"error": {"code": "acl_denied", "message": "preflight failed: …", "hint": "add to your tailnet policy:\n  \"tagOwners\": …", "doc_url": "https://github.com/rosskukulinski/twd/blob/master/docs/errors.md#acl_denied"}}
```

| Field | Always | |
|---|---|---|
| `code` | yes | a code from [errors.md](errors.md); act on this, not on the message |
| `message` | yes | one line for a human |
| `hint` | yes | what to do next: the fix lines (joined with newlines), a usage line, or where to read about the code |
| `doc_url` | yes | the code's entry in errors.md |
| `detail` | no | more lines of context (compiler output, the request that failed) |
| `fix` | no | `hint` as a list of lines |
| `trace_id` | no | correlates with `twd audit --trace <id>` |

The exit code is 1 when a command fails (including a bad flag or a missing
argument, code `bad_request`) and 2 for an unknown command. With `--json`,
both are JSON. Key material is redacted from everything twd prints.

`twd mcp` (without `--config`) is not a `--json` command: its stdout is the
MCP stdio protocol (newline-delimited JSON-RPC). A refusal from mgmt
becomes a JSON-RPC error whose `data` is the error object above.

`twd doctor` is the one command that prints a result *and* fails: its report
is on stdout, then `doctor_failed` is on stderr if a check failed.

## Event streams (NDJSON)

Commands that take a while, or don't end on their own, print one compact JSON
object per line. Every line has `event` (its kind) and `ts` (RFC 3339, UTC,
when twd wrote it).

### `twd deploy --json`: progress on stderr

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
| `ready` | `deploymentID`, `host`, `magicDNS`, `url` |
| `public_wait`, `public` | `url` (with `--wait-public`) |

A failure ends the stream with the usual `{"error": …}` line instead.

### `twd dev --json`: events on stdout

Without `--once`, `twd dev` runs until it is stopped, so its output is a
stream on **stdout**:

| `event` | Fields |
|---|---|
| `ready` | `url`, `serves` (with the MCP route), `workload`, `node`, `as`, `tags`, `timingsMs` |
| `reload` | `workload`, `node`, `ms` (after a file change) |
| `request` | `record`: one request record, as in `twd logs --json` (`$defs/logs.record`), with its console lines and exceptions |
| `error` | `error` (a rebuild failed; the last good version keeps running) |
| `stopping` | (Ctrl-C or SIGTERM) |

`twd dev --once --json` is a single result document instead (`$defs/dev`).

### `twd logs --json`: one line per record or log line on stdout

`twd logs <workload> --json` prints the workload's request records, one
per line (`$defs/logs.record`: `time`, `kind`, `method`, `path`, `status`,
`durationMs`, `outcome`, `object`, `caller`, `traceId`, `logs`,
`exceptions`, …). Raw output (`--console`, `--component`) is one event per
line (`$defs/logs.line`):

```json
{"event": "log", "ts": "…", "workload": "hello-mcp", "line": "…"}
```

`workload` is set for `twd logs <workload> --console`, `component` for
`--component mgmt|hostd|supervisor`. From a machine linked with `twd link`,
replica lines also carry the `host` they came from. With `-f`, lines keep
coming until the command is stopped.

## Confirmations

`--json` never prompts. Commands that ask before acting (`twd up`,
`twd agent setup`, `twd agent remove`) fail without `--yes` when there's no
terminal or `--json` is given: `twd up` with `bad_request`, `twd agent` with
`confirmation_required`. See
[Using twd with a coding agent](agents.md#safety-rules-the-skills-give-agents).

## Tests

`cmd/twd:TestCLIJSONContract` runs every command and subcommand with
`--json` and checks results against the schema and errors against this page.
It fails if a command has no schema, or the schema has one that's gone.
`cmd/twd:TestAgentEval` checks the successful outputs against a running
twd, including the deploy events. `cmd/twd:TestDevJSONEvents` checks the
`twd dev` stream.
