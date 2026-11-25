---
kind: feature
upgrade: []
---
`tw <command> --help` for a command with subcommands (`tenant`, `host`,
`d1`, `kv`, `queues`, `r2`, `secret`, `docs`, `agent`, `image`,
`telemetry`, `notify`, `upgrade`, `release`, `custody`, `expose`) lists
each subcommand's usage on its own line instead of one long line, and
says `tw <command> <subcommand> --help` shows its flags; `tw help --all`
does the same. `tw tenant --help` no longer spells out every
`tenant set` flag (`tw tenant set --help` and docs/limits.md list them),
and `host`, `image` and `upgrade` show `[flags]` in their usage lines.
Running such a command without a subcommand names them all, from the
same list. docs/cli.md links a subcommand whose flags are the same as an
earlier section's ("Same flags as tw d1") instead of repeating the block,
which takes about 670 lines out of it.
