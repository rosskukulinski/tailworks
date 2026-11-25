# mgmt store migrations

One file per migration (ADR-041 §3). Migrations apply in
ID order, once each; `schema_migrations` records which ran.

- **Name:** `<UTC yyyymmddhhmm>_<slug>.sql`, for example
  `202611151900_host_versions.sql`. Use the time you write it. Timestamps
  plus slugs don't collide across parallel branches, and a branch that
  merges late with an older timestamp still applies: every migration must
  be valid on its own, whatever ran before it.
- **Dialects:** `<id>.sql` runs on both. Where SQLite and Postgres differ,
  write `<id>.sqlite.sql` and `<id>.pg.sql` instead. `{{blob}}` becomes
  `BLOB` (SQLite) or `BYTEA` (Postgres).
- **Expand only, by default:** add tables, columns with a default or
  nullable, indexes. An older mgmt binary (N−1) keeps working against the
  expanded schema.
- **Contract** (drop, rename, tighten a constraint): put `-- tw:contract`
  on its own line. It ships no earlier than the release after the one that
  stopped using the old shape, and it raises the store's minimum binary
  version to the applying binary's version, so an older binary refuses to
  start against it (`version_skew`). `scripts/check-changes.sh` requires a
  `schema-contract` release-note fragment for it.
- **Never edit a migration that has merged to master.** Add a new one.

Everything that predates this directory is the baseline
(`000000000000_baseline`): the idempotent DDL in `sqlstore.go` `migrate()`.
Don't add to that DDL or to `addColumns` any more.
