---
kind: feature
upgrade: [cli-json]
---
`tw secret bulk [<file>]` sets several of a workload's secrets at once,
like `wrangler secret bulk`: from a dotenv file (`tw secret bulk
.dev.vars`) or a JSON object of strings, or from piped stdin. It checks
every name, size and the workload's total before setting anything, never
prints a value, and only adds and replaces (secrets not in the input are
kept). If one PUT fails it stops and names the secrets set, failed and not
attempted. `--json` returns those three lists (`$defs/"secret bulk"`).

`tw notify set` and `tw telemetry set` now list their own flags
(`--webhook`, `--otlp`, `--header`, …) only under `set`: `tw notify
--help`, `tw notify rm --help` and the telemetry equivalents no longer show
them, and passing one to another verb is the usual unknown-flag error.
