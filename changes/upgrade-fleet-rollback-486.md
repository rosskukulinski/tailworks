---
kind: breaking
upgrade: [cli-json, config-default, protocol]
---
`tw upgrade` has one job per subcommand now, and a rollback command.

- `tw upgrade [<version>]` upgrades this machine, with the flags that
  matter there (`--from`, `--plan`, `--yes`, `--allow-unsigned`;
  `--prefix`, `--health-timeout` and `--no-rollback` under Advanced). On a
  linked machine it still upgrades the fleet, and refuses the local-only
  flags with `bad_request` naming `tw upgrade fleet`.
- `tw upgrade fleet [<version>]` plans and runs a fleet upgrade (`--hosts`,
  `--canary`, `--soak`, `--settle`, `--no-wait`), and `tw upgrade fleet
  status|pause|resume|abort [<run-id>]` controls the run.
- `tw upgrade import` and `tw upgrade releases` replace `release import`
  and `release ls`. `tw upgrade import <version>` downloads the release's
  `SHA256SUMS`, checks `SHA256SUMS.sig` against the pinned release keys
  (always), downloads the tarball for each platform the self-upgrading
  hosts run (`--platform` names them instead), and imports it. Nothing
  fetches a release implicitly.
- `tw upgrade rollback [<backup-dir>] [--restore]` puts the release
  `install.sh` kept as `.prev` back, with tailworks stopped, restarted and
  health-checked. It restores the store too (the newest backup `tw upgrade`
  took of the previous release, then the restore fence) when the previous
  tw-mgmt can't open the store, or with `--restore`; then it asks you to
  type `rollback`. It keeps the current state in
  `<state>/backups/<time>-before-rollback` first. It replaces the five-step
  "roll back by hand" sequence, and every error and doc that printed it.

**Breaking:** the old spellings (`--fleet`, `status|pause|resume|abort`
straight after `tw upgrade`, and the `release` command) still work for this
release, hidden, and print the new spelling on stderr; they go away in the
next one. In `schema/cli.schema.json`, `$defs/release ls` is now
`$defs/upgrade releases`, `$defs/release import` is `$defs/upgrade import`,
and `$defs/upgrade rollback` is new.

Operator action: in scripts, replace `tw upgrade --fleet` with `tw upgrade
fleet`, `tw upgrade status|pause|resume|abort` with `tw upgrade fleet
status|pause|resume|abort`, and the `release ls|import` command with
`tw upgrade releases|import`.
