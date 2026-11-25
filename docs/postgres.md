# Postgres and mgmt replicas

`tw up` keeps mgmt's store in SQLite (`~/.tailworks/mgmt/mgmt.db`) and its
secrets in file custody (`~/.tailworks/mgmt/custody`). This page covers
four steps beyond that:

- move a running install's store to Postgres,
- back up and restore an install whose store is Postgres,
- move file custody to setec,
- run several mgmt replicas (HA).

The store is the only authoritative copy of every tenant's KV, D1, R2
metadata and Queue messages. Hosts keep caches of it, so plan its backups
before you plan anything else.

## When to switch

SQLite is the right store for one machine, or for one mgmt with a few
hosts. `tw backup` copies it while tailworks runs.

Move to Postgres when:

- the store's disk must outlive the machine (a managed database, its own
  backups and point-in-time recovery), or
- you want more than one mgmt replica.

Postgres on its own is still one mgmt. If that mgmt stops, workloads keep
serving, but new deploys, kills and store writes wait until it is back. Only
several replicas (below) keep mgmt answering while one is down.

## Move a tw up install to Postgres

You need an empty Postgres database that this machine can reach (the tests
run Postgres 17), and the `tw-mgmt` binary from the same release as `tw`
(the install puts both in `bin/`).

1. Put the DSN in a file only you can read. With `--db-file`, the password
   stays out of `tw.json` and off every command line:
   ```sh
   umask 077
   echo 'postgres://tw:…@db.example:5432/tailworks?sslmode=require' > ~/.config/tailworks/pg.dsn
   ```
2. Take a backup, so you can go back:
   ```sh
   tw backup --out ~/tw-before-postgres
   ```
3. Stop tailworks, copy the store, and start on Postgres:
   ```sh
   tw down
   tw-mgmt migrate --from sqlite --state-dir ~/.tailworks/mgmt --to-file ~/.config/tailworks/pg.dsn
   tw up --db-file ~/.config/tailworks/pg.dsn
   tw status
   ```
   `tw up` remembers `--db-file`, so later `tw up` runs (and `tw upgrade`)
   keep using Postgres.

`tw-mgmt migrate --from sqlite`:

- **Leaves the SQLite store unchanged.** It copies a snapshot of it.
- **Refuses while a tw-mgmt is serving the SQLite store.** Stop tailworks
  first, so no write lands after the copy.
- **Needs an empty target.** It refuses a database that already holds
  tailworks data.
- **Copies every table in one transaction.** That includes tenants,
  deployments, objects and leases, the KV, D1 and R2 journals, Queue
  messages, the custody index and the audit chain. Rows are copied as they
  are stored: journal entries stay sealed under their keys in custody, and
  the audit chain keeps its hashes (`tw audit --all` still verifies).
- **Runs the restore fence on the target.** Every lease epoch moves past
  any epoch a host could still hold.

Custody stays where it was (file custody in `~/.tailworks/mgmt/custody`),
so keep the state directory. Keep `mgmt.db` too until Postgres has served
for a while; after the move, nothing reads it.

`tw up` refuses `--db-file` when this install has a SQLite store and the
Postgres database was never migrated. That way, starting on an empty store
can't silently lose every tenant.

**Going back.** `tw down`, `tw restore ~/tw-before-postgres --yes`, then
`tw up`. The backup's `tw.json` has no `--db-file`, so mgmt runs on SQLite
again. Every change made on Postgres is lost.

## Back up and restore with Postgres

`tw backup` on a Postgres install copies custody, the ID secret, mgmt's
node state and `tw.json`, but not the database. Its `backup.json` says
`"store": "postgres"`. Dump the database first, then take the backup, so
custody holds every key the dump refers to:

```sh
pg_dump -Fc -d "$(cat ~/.config/tailworks/pg.dsn)" -f ~/tw-pg-$(date +%F).dump
tw backup --out ~/tw-backup-$(date +%F)
```

To restore:

```sh
tw down
tw restore ~/tw-backup-2026-11-21 --yes
pg_restore --clean --if-exists -d "$(cat ~/.config/tailworks/pg.dsn)" ~/tw-pg-2026-11-21.dump
tw-mgmt restore-fence -db-file ~/.config/tailworks/pg.dsn
tw up
```

Run `tw-mgmt restore-fence` after every restore of the database, including
your provider's point-in-time recovery. The store went back in time, and
the fence moves every lease epoch past the ones handed out since. Without
it, a fencing token could be issued twice. Store objects on hosts whose
copy is ahead of the restored journal start over from it on their next
sync.

## Several mgmt replicas (HA)

`tw up` runs one mgmt replica, embedded in its process with this machine's
host. It doesn't host the `svc:tw-mgmt` Service, and its node is named
`tw-mgmt`, the label hosts would use for the Service. So HA means running
`tw-mgmt` directly on each replica machine and `tw-hostd` on each host,
rather than `tw up`. Each replica needs:

- **The same Postgres store:** `-db-file <file>`.
- **The same setec custody:** `-custody setec -setec-url <url>`. File
  custody lives on one machine, so mgmt refuses to start a second replica
  that uses it.
- **Its own node name** (`-tsnet-hostname tw-mgmt-1`, `tw-mgmt-2`, …) and
  `-tsnet-service svc:tw-mgmt`.
- **A shared registry:** `-registry-dir` on storage every replica mounts,
  or `-registry-dir off` if you don't deploy container images.

Hosts reach whichever replica is healthy:
`tw-hostd -mgmt http://tw-mgmt.<tailnet> …`. Commands, the tailnet policy
and the health gate are in [services.md](services.md#mgmt-replicas-svctw-mgmt).

**Custody comes first.** Secrets and the keys that seal store data stay in
the custody they were created in. On an existing install, `tw up` refuses
to change `--custody`. Move an install that started on file custody with
`tw custody migrate` ([below](#move-file-custody-to-setec)), or start a new
one with `tw up --custody setec --setec-url <url> …`.

## Move file custody to setec

File custody keeps one sealed file per secret, named by a hash of the
secret's name, on one machine. Several mgmt replicas need setec instead.
`tw custody migrate` moves a `tw up` install's custody there:

```sh
tw down
tw custody migrate --setec-url https://secrets.<tailnet>.ts.net --dry-run
tw custody migrate --setec-url https://secrets.<tailnet>.ts.net
tw up
```

The machine must reach setec, and setec must grant this machine's node
put, activate and get on the `tw/` names, as for `tw up --custody setec`.

`tw custody migrate`:

- **Needs tailworks stopped.** A secret written during the copy would land
  in file custody only.
- **Names every entry first.** File custody can't list its names, so the
  command derives every name it could hold from mgmt's store: the
  `custody_names` index, and the rows that imply a name (each workload's
  secrets and volume key, each KV, D1, R2 and Queue data key, each object's
  and warm pool's auth key, each tenant's notification and telemetry
  settings and OBO root key, the shared ID secret). A never-deployed
  workload's secrets are named by the audit records of their changes. A
  name counts only when its file decrypts under it, which proves the
  match.
- **Backfills `custody_names`.** mgmt indexes each tenant name before it
  writes it, but entries written before the index existed weren't. The
  command indexes them, so the index covers every entry. The ID secret is
  install-wide and stays out of it; its `secret_choice` row names it.
- **Refuses an entry it can't name** (`custody_unmapped`) and copies
  nothing. Nothing in tailworks reads such an entry, since no row names it:
  it's left by an interrupted write, or by data deleted before the index
  existed (for a never-deployed workload's secrets, also audit records
  pruned by retention before then). The error lists the files. Move them
  out of `~/.tailworks/mgmt/custody/secrets`, then run the command again.
  Leftover `*.tmp` files are unfinished writes and are skipped.
- **Refuses a setec that holds another value** under one of the names, and
  copies nothing. An entry already there with the same value is kept, so
  you can run the command again after an interruption.
- **Verifies the copy.** It reads every entry back from setec and compares.
- **Then switches `tw.json`** to `custody: setec` and the URL, after it
  backs up the state to `~/.tailworks/backups/<time>-before-custody-migrate`.

File custody is left as it was. Remove `~/.tailworks/mgmt/custody` once
tailworks has served from setec for a while.

**Going back.** `tw down`, `tw restore <the before-custody-migrate backup>
--yes`, then `tw up`. As with any restore, every change made since the
backup is lost. With a Postgres store, `tw restore` leaves the database as
it is, but secrets and store keys created on setec aren't in file custody:
go back before you create any.
