---
kind: feature
upgrade: [cli-json]
---
`tw backup [--out <dir>]` backs up a running install: a consistent copy of
mgmt's SQLite store, custody, the ID secret, mgmt's node state and
`tw.json`, with a `backup.json` that says what is and isn't included.
`tw restore <dir>` puts a backup (or a `tw upgrade` backup) back with
tailworks stopped. It keeps the state it replaces and runs the restore
fence.

`tw-mgmt migrate --from sqlite --to-file <dsn file>` copies a stopped
install's SQLite store into an empty Postgres database in one transaction,
then fences it. `tw up --db-file <file>` (or `--db`) runs mgmt on that
store, and `--custody setec --setec-url` uses setec custody on a new
install. docs/postgres.md covers when to switch, backups with Postgres, and
HA replicas, which still run `tw-mgmt` directly.

A store object on a host whose copy is ahead of mgmt's journal (after a
restore or an upgrade's rollback) now starts over from the journal.
Before, its writes kept failing with `store_conflict`.
