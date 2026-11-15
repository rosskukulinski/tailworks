# Upgrading twd

Every part of twd ships in one release with one version: the `twd` CLI,
`twd-mgmt`, `twd-hostd`, `twd-vmhelper`, the microVM guest image, workerd,
the admin UI and the `@twd/*` packages. This page covers what may run
together, how to upgrade a `twd up` machine, and how to upgrade a fleet. The
design is in [ADR-041](design/adr/041-upgrades-and-releases.md),
and the maintainer side is in [RELEASING.md](RELEASING.md).

## What may run together

Before 1.0, a **release line** is a minor version (0.2, 0.3, …). From 1.0
on, it is a major version.

| Component | May run with twd-mgmt N | If not |
|---|---|---|
| twd-hostd | N−1 or N, never newer than mgmt | refused at registration: `409 version_skew` |
| other twd-mgmt replicas | N−1 and N together, during a roll | the newer replica refuses to start |
| twd CLI | N−1 or newer | a warning on stderr |
| guest image, platform runtime, admin UI | always the same release as the hostd or mgmt that ships them | — |

- **Hosts upgrade after mgmt.** A host that is out of the window keeps
  heartbeating, and its running workloads aren't disturbed, but placement
  skips it. `twd status --json` lists it under `versions.skewed`.
- **Features gate new work.** Hosts report their features when they
  register (`d1`, `kv`, `queues`, `r2`, `telemetry`, `workflows`, `self-upgrade`, …). A deployment
  that uses a KV namespace is placed only on hosts that offer `kv`. When none
  does, the deploy fails with `feature_unavailable`, which names the hosts to
  upgrade.
- **Every response from `GET /v1/status` and every `/v1` call carries
  mgmt's version:** `status.versions` and the `Twd-Version` response header.

## A `twd up` machine

```sh
twd upgrade --plan              # what would change: notes, breaking changes, migrations
twd upgrade                     # the latest published release
twd upgrade 0.3.1               # a given release
twd upgrade --from twd-0.3.1-linux-amd64.tar.gz   # an offline copy (SHA256SUMS next to it)
```

`twd upgrade` runs these steps:

1. It downloads the release, or takes `--from`, and checks its SHA-256.
2. It shows the plan from the release's catalog: breaking changes, operator
   actions, contract migrations and the workerd change.
3. It backs up mgmt's store together with custody (and the issuer and ID
   secrets, the node state and `twd.json`) into `~/.twd/backups/<time>-<from>`.
   The store and custody are only valid together, because platform store,
   queue and secret data is sealed by custody keys.
4. It stops twd, installs the release (`install.sh` keeps the previous one
   as `bin.prev` and `share.prev`), and starts twd again. A `twd up --service`
   install is restarted through systemd or launchd.
5. It waits for mgmt and the host to report the new version and for every
   deployment to be `active`.
6. If that doesn't happen within `--health-timeout` (3 minutes by default),
   it rolls back by itself:
   - stops twd;
   - restores the backup;
   - swaps `.prev` back;
   - fences the restored store (lease epochs jump, so no fencing token is
     ever reused);
   - starts the previous release.

There is no automatic rollback once a **contract migration** has run,
because the previous twd-mgmt can't open the contracted store. In that case
`twd upgrade` stops and prints the restore command. Run `twd upgrade --plan`
to see contract migrations before you start.

To roll back by hand:

```sh
twd down
cp -R ~/.twd/backups/<time>-<from>/mgmt/. ~/.twd/mgmt/   # after removing mgmt.db, mgmt.db-wal, mgmt.db-shm
~/.twd/install.sh --rollback
twd-mgmt restore-fence -state-dir ~/.twd/mgmt
twd up
```

**The microVM tier's helper** is root-owned. `twd upgrade` doesn't touch it.
When `twd doctor` reports its protocol out of range, run
`sudo twd host setup --microvm`. A helper serves its own protocol version and
the one before it, so hostd and the helper can be upgraded in either order
within one release line.

## A fleet (twd-mgmt replicas and remote hosts)

From a machine linked to twd-mgmt (`twd link`), or with `--fleet`:

```sh
twd release import twd-0.3.1-linux-amd64.tar.gz twd-0.3.1-linux-arm64.tar.gz   # once per release
twd upgrade --plan 0.3.1        # mgmt plans it from its catalog and the fleet's versions
twd upgrade 0.3.1               # start the upgrade run; follows it until done (--no-wait returns)
twd upgrade status | pause | resume | abort
```

- **The release catalog.** twd-mgmt knows every release up to its own
  without network access. `twd release import` adds a newer release from its
  tarball: the notes, and the tarball itself, which hosts download.
  `twd release ls` lists the catalog.
- **The upgrade run.** The run is stored in mgmt's database and driven by
  whichever replica holds its lease. Closing the laptop doesn't stop it, and
  a replica that dies hands it to another. Its steps:
  1. **backup:** mgmt can't verify a backup. Take one of the store
     (point-in-time recovery, or a dump) together with custody before you
     start.
  2. **mgmt:** roll every twd-mgmt replica to the target with your
     deployment system. The run waits until every live replica reports it.
  3. **contract:** contract migrations deferred while an older replica was
     live are applied now.
  4. **hosts, canary first:** the run moves through these phases for each
     host:
     - cordon the host;
     - drain it, if every workload on it can move (stateless);
     - push the upgrade to it;
     - wait for it to re-register at the target;
     - settle: every deployment on it must be `active`;
     - soak: the canary only, `--soak`, 5 minutes by default;
     - uncordon it.

     A host started with `twd-hostd -self-upgrade` installs the pushed
     release itself and restarts. Run it under a service manager that
     restarts it: it exits with status 75. Any other host shows an operator
     step ("install twd X on host h and restart twd-hostd"), and the run
     continues once it re-registers.
- **When a gate fails,** for example a host that doesn't come back within
  `--settle` or a canary that fails its soak, the run **pauses** with the
  reason. Fix the cause, then run `twd upgrade resume`. `abort` ends the run
  and uncordons the host in progress.
- **More than one release line.** A fleet more than one line behind the
  target, or a target past a release with contract migrations, needs an
  intermediate upgrade. The plan names the release to go through.

## Hosts by hand

```sh
twd host cordon h1      # no new placements; h1 keeps what it runs
twd host drain h1       # cordon, then move every workload that can move
twd host drain h1 --dry-run
twd host uncordon h1
```

- **Workloads with Durable Objects or Workflows keep their storage on the host's disk.**
  A drain leaves them in place, so restart the host in place instead (about
  a minute until its objects rejoin).
- **`--allow-local-state-loss`** moves them anyway, and their objects start
  empty on the new host.
- **Data that survives a move:** platform stores (KV, D1, the R2 index),
  secrets, and R2 bytes on the `s3` backend live outside the host.

## Stores and data formats

- **Store migrations.** twd-mgmt migrates its store when it opens it.
  Migrations are numbered, and each runs once. `twd-mgmt migrate --check`
  shows what is pending without changing anything.
- **Expand migrations** add tables or columns. They apply at once, and the
  previous twd-mgmt keeps working against the expanded store, so replicas can
  roll and a rollback without a restore works.
- **Contract migrations** drop or rename. They wait until every replica runs
  the new version, then raise the store's minimum twd-mgmt version. An older
  twd-mgmt refuses to start against the store (`version_skew`); restore the
  pre-upgrade backup to go back.
- **Data formats on hosts and in the store** carry a version: `objects.json`,
  node `object.json`, `twd.json`, and the KV, D1 and R2 journals. A twd reads
  every older format and refuses a newer one with "written by a newer twd;
  upgrade".

## Versions, compatibility dates and the changelog

- **What changed:** [CHANGELOG.md](../CHANGELOG.md), newest release first,
  and the same notes on each
  [GitHub release](https://github.com/rosskukulinski/twd/releases).
  Breaking changes and the steps an operator must take are in each
  release's "Breaking" and "Upgrade actions" sections.
- **What a version promises:** before 1.0 a minor release may break things
  (with a migration path and a "Breaking" note) and a patch only fixes
  bugs; from 1.0 on, twd follows SemVer. Details:
  [RELEASING.md](RELEASING.md#versions).
- **Your app's `compatibility_date` and `compatibility_flags`** go to
  workerd unchanged, so a Worker behaves as it would on Cloudflare at that
  date, within the workerd version the release pins (CHANGELOG.md names
  it). twd adds one flag, `nodejs_als`, unless you already set
  `nodejs_als`, `nodejs_compat` or `nodejs_compat_v2`
  (`hostd/gen/observe.go`). twd's own
  platform worker has its own pinned date; a release that moves it, or
  bumps workerd, says so under "Upgrade actions".
