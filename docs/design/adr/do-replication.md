# ADR-do-replication: Replicate Durable Object storage with workerd's own WAL capture and commit gate (spike #297)

**Date:** 2026-11-24
**Status:** Proposed. This is the result of the time-boxed spike in
[#297](https://github.com/rosskukulinski/tailworks/issues/297) and a
recommendation for [#38](https://github.com/rosskukulinski/tailworks/issues/38).
It is not a production design.

**Question.** Can tailworks replicate Durable Object SQLite storage the way
Cloudflare's Storage Relay Service does, using the open-source workerd it
pins? That means shipping WAL frames off the host and holding a DO's output
until the frames are confirmed.

**Short answer.** Yes, and most of the work is already done upstream. The
pinned workerd (1.20261121.1, commit `d684138`) has an experimental
**cluster mode**. It captures each committed transaction's pages from the
WAL through workerd's own VFS, uploads them to an S3-compatible or GCS
bucket, and holds the output gate until the bucket has them and the
object's ownership epoch still holds. When the upload fails, the output
gate breaks and the object restarts. A new owner restores the object from
the bucket's snapshot and log before workerd opens it, one object at a
time. The frame format is SRS's own (`frames.capnp` has an `srsTools`
snapshot justification). Stock workerd has no other way in: the commit hook
is a C++ constructor argument that only cluster mode fills.

So the choice for #38 is not "write a VFS hook" but "adopt workerd's
cluster mode, and on what terms". Five things stand in the way (§Gaps). The
biggest is that a cluster instance exits when the bucket stalls for about
8 s, and tailworks runs a workload's stateless Workers in the same workerd
process.

## Evidence

**workerd source.** Tag `v1.20261121.1` (the pin in `docs/COMPAT.md` and
`cmd/tw/state.go`; #297 was filed against 1.20261118.1). It was cloned
read-only, not built or run from source. Paths are relative to
`src/workerd/`. Line numbers are at that tag.

**tailworks, observed.** `tw dev` (built from this branch, real hostd,
stock workerd 1.20261121.1) on a scratch app. The app has a plain SQLite DO
class (`Plain`), a DO class with its own tailnet identity (`Tagged`, which
tailworks runs as a facet of a platform class), and a D1 binding. It made
writes of about 3 KB rows, and the storage directory was inspected while
the objects were live and again after they went idle.

## Q1. WAL access

### What is on disk under hostd

One directory per workload,
`<data>/tenants/<tenant>/workloads/<workload>/do/`, which `hostd/gen/gen.go`
(lines 388, 425, 471) passes to workerd as the `do-storage` disk service.
Inside it there is one directory per DO namespace `uniqueKey`, and one set
of files per object:

```
do/tw-spike297-Plain/
  <actorIdHex>.sqlite         root actor's database (tailworks' platform wrapper; 4 KiB, empty)
  <actorIdHex>.1.sqlite       facet 1: the tenant class's own storage (table t)
  <actorIdHex>.1.sqlite-wal   only while the object is open
  <actorIdHex>.1.sqlite-shm   only while the object is open
  <actorIdHex>.facets         facet index ("app" -> 1)
  metadata.sqlite(-wal,-shm)  the namespace's alarm index (_cf_ALARM)
do/tw-spike297-Tagged/        same shape
do/tw-spike297-__tw_store/    platform stores: the D1 database is one object
  <actorIdHex>.sqlite         (_cf_KV, d1_migrations, the app's tables)
```

- Every tailworks DO is a root actor with the tenant's code in facet 1. So
  a replicated object is always two or more databases. Cluster mode handles
  that: each facet's database is a "lane" of the root's log
  (`server/server/actor.rs:1426-1433`).
- The paths come from `ActorStorage::Impl::getSqlitePath`
  (`server/factory/worker-factory-actor.c++:197-203`). Every database is put
  in WAL mode with `PRAGMA journal_mode=WAL` (`:553-561`), checked with
  `PRAGMA journal_mode` on copies (`wal` for all of them).
- WAL frames are 4 KiB pages plus a 24-byte header. Fifty inserts in one
  request gave 54 frames and 2 commit frames, one for the constructor's
  `CREATE TABLE` and one for the request's implicit transaction.
- When the object went idle, its `-wal` and `-shm` files were gone: closing
  the last connection checkpoints and deletes them (`util/sqlite.h:315`).

### Hook options, rated for stability across workerd upgrades

| Option | What it sees | Can it gate output? | Stability |
|---|---|---|---|
| **A. workerd cluster mode, as shipped** | Every WAL write, synchronously, in process | Yes, the output gate waits for the bucket | **Medium-low.** It is config-level, so there's nothing to rebase. But it needs `--experimental`. The bucket layout is versioned (`v1/`, `server/cluster/lib.rs:9-11`). The directory saw 49 commits between 2026-10-29 and 2026-11-20, 8 of them between the 1.20261118.1 and 1.20261121.1 tags. `frames.capnp` has been stable since 2026-10-30. |
| **B. A patch we carry**, for example our own `Committer` that calls hostd | Same | Yes | **Low.** tailworks ships stock workerd from npm (`Makefile:6`, `:25`). A patch means building workerd (Bazel and V8) for every platform at every bump, against files that change weekly (`worker-factory-actor.c++` was created 2026-10-13 and has 14 commits since). |
| **C. Upstream: a public commit hook** (a config `ServiceDesignator` the commit waits on) | Same | Yes | **High once merged.** The C++ seam already exists and is documented for exactly this: `ActorSqlite`'s `commitCallback` "can be used e.g. when the database needs to be replicated" (`io/actor-sqlite.h:58-60`; the file dates from 2023-02, much older than cluster mode). The work is the config surface and review. |
| **D. Tail `-wal` from hostd** (the Litestream approach) | Frames after the fact, by polling | **No**: by the time it reads them, the client has its response | **Medium.** It uses only SQLite's documented file format and the path layout above, which is workerd-private. It has to hold a read transaction to stop checkpoints from overwriting frames it hasn't shipped (Q4). The WAL also disappears when the object closes. That costs RPO > 0, and it races. |
| **E. `sqlite3_wal_hook` / `sqlite3_commit_hook`** | A callback per commit | Only with a patch | None. Workerd doesn't expose them, so this is option B. |

## Q2. Output gate waiting on an external ack

**It already does, in cluster mode. It's not configurable outside it.**

- `ActorSqlite` takes a `commitCallback`. After each commit it calls the
  callback and awaits the returned promise (`io/actor-sqlite.c++:607-611`).
  A must-confirm write locks the output gate on that commit
  (`outputGate.lockWhile(lastCommit…)`, `:405-411`).
- Workerd's server wires the callback to `READY_NOW` unless the namespace
  is replicated. When it is, the callback is `Committer::commit`
  (`server/factory/worker-factory-actor.c++:566-573`, `:697`).
  `replicated` is `ns.cluster().is_some()` (`server/server/actor.rs:1457`).
  That holds for every disk-backed namespace in a process with a `cluster`
  config (`:207-209`), so it is all or nothing per process.
- `Committer::commit` → `Cluster::sync_actor` → `Owners::commit`
  (`server/server/cluster.rs:1073-1093`; `server/cluster/owner.rs:357-377`).
  Commits are grouped: one upload carries everything captured so far for
  all waiting commits.
- The ack (`owner.rs:381-400`) needs two things. First, PUT the batch to
  `do/<ns>/<actor>/log/<epoch>/…`. Then re-read `own.json` and check that
  it still names this epoch (`ownership.rs:445-452`, "spec: `TierAck`").
  So each group commit costs at least one PUT and one GET against the
  bucket.
- Changes to the facet tree (delete or clone a facet) also lock output
  until they are durable (`worker-factory-actor.c++:452-466`).

What it would take to wait on *our* replication rather than a bucket:

- **Config option:** none exists. The only target is
  `ClusterConfig.bucket` (`s3://` or `gs://`; `server/cluster/cluster.rs`
  `open_bucket`, lines 567-596).
- **Small patch:** a second `Committer` implementation. It is small in
  lines but large in cost (option B above).
- **Upstream change:** a public hook (option C). Or, without any change,
  make "our replication" *be* an S3-compatible endpoint. That endpoint
  needs conditional PUT (`If-None-Match`, `If-Match`), GET, LIST and
  DELETE. Workerd tests for conditional writes at startup and refuses a
  store that ignores them (`cluster.rs:603-641`).

## Q3. Failure semantics

**Cloudflare's behaviour is what cluster mode does.**

- **The commit fails** (an upload error, or the epoch was superseded). The
  promise rejects, the output gate breaks, and the actor is marked broken
  (`actor-sqlite.c++:296-313` and `:647-660`, `taskFailed`). Gated
  responses and outbound messages are never released. The caller gets an
  error, and the next request starts a fresh actor.
- **The local file is not trusted after that.** On eviction the object is
  kept "dormant" (reopened from its local file without a restore) only if
  the bucket holds every captured commit (`owner.rs:572-596`:
  `guard.tiered >= last`). Otherwise the next start restores from the
  bucket and wipes the local files (`owner.rs:222-245`, `layout.clear()` at :231).
  So no client is served state that was never confirmed.
- **"Failed" means "may have run".** A failed commit's frames stay
  captured. An eviction later uploads what the bucket lacks
  (`owner.rs:456-472`, `tier_one`). If the bucket comes back, the write can
  become durable although its caller saw an error. If the epoch moved on,
  the frames land in the old epoch's session, which nothing reads (Q6).
  This matches Cloudflare's documented semantics: an error doesn't prove
  the write was lost.
- **Capture loss.** A WAL write that the capture can't account for marks
  the capture lost, and every later commit then fails
  (`capture.rs:205-207`, `:222-226`, `:274-281`). A missed write can only
  fail closed, never silently ship a torn database.
- **Timeouts.** There's no commit deadline in config. A commit waits on the
  bucket client's request bounds. The node lease is renewed every 3 s with
  a 20 s lease (`server/cluster/logic/lease.rs:20-27`, `:78-90`). The node
  "rides out about 8 s of bucket trouble and fences while its lease
  holds".
- **Fencing exits the process.** It calls `cli_exit(1)`
  (`server/server/cluster.rs:515-540`), and an event loop stalled for 60 s
  fences too (`lease.rs:67-74`, `:88`). This is the OPS-1 finding that
  ADR-012 recorded, and it's still true at this tag.

## Q4. Checkpointing

- **Workerd doesn't control checkpoints.** It sets no `wal_autocheckpoint`
  (a grep finds the pragma only in the cluster tests), so SQLite's default
  of 1000 pages applies. Observed: the WAL of one object grew to 5,005,832
  bytes (1215 frames). Then the checkpoint sequence went from 0 to 1, the
  salts changed, and SQLite rewrote the same file from offset 32 without
  truncating it. The only explicit checkpoint is
  `wal_checkpoint(TRUNCATE)`, on a cloned facet in cluster mode
  (`worker-factory-actor.c++:250-258`).
- **In cluster mode, checkpoints can't lose unshipped frames.** The capture
  copies each WAL write as it happens (`WalFile::write`,
  `worker-factory-actor.c++:136-140` → `capture.rs:210-243`). It cuts
  transactions at commit frames and keeps them in memory, and in a local
  `.log` cache before upload (`owner.rs:794-806`), until the bucket acks
  them. A write at offset 0 is read as "a checkpoint started a new WAL"
  (`capture.rs:215-222`). So no checkpoint hold is needed. The cost is
  that memory and the cache grow while the bucket is unreachable, until the
  node fences.
- **From outside (option D), hostd can't stop a checkpoint.** It would have
  to hold a read transaction through the shared `-shm`. That works because
  workerd uses SQLite's native VFS and locking on a real directory
  (`util/sqlite.h:909-912`). `tw backup` already reads live DO databases
  this way (`cmd/tw/backup_hostd.go`). But a reader that holds checkpoints
  back lets the WAL grow without bound if it stalls. Hostd can detect a
  reset by the WAL header's checkpoint sequence and salts, but it can't
  recover the overwritten frames.

## Q5. Recovery before open, per object

**Yes, per object, without restarting workerd.** This is built.

- A request to an object that this instance doesn't own claims it with a
  conditional write of `own.json`. Taking it over from an expired owner
  bumps the epoch (`server/cluster/lib.rs:14-20`, `ownership.rs:21-43`).
- Before the actor's constructor runs, `prepare_storage` awaits
  `Owned::restore` (`server/server/actor.rs:1394-1424`). That calls
  `Owners::take_over` (`owner.rs:182-260`). It clears the local files,
  downloads the last opened epoch's snapshot and log, replays them into a
  spool, moves each lane's database into place, and writes the new epoch's
  starting snapshot. If the restore fails, the object leaves the namespace,
  and the next request claims it again.
- A clean stop hands objects off. On SIGTERM the instance uploads what's
  missing and releases each object for the next claim, within
  `stopTimeoutMs` (default 8000; `server/workerd.capnp:275-316`). After a
  crash, a peer takes over when the dead node's 20 s lease expires.
- **Migration limit:** a `localDisk` that already holds DOs from before
  cluster mode "is refused at startup; they are not imported"
  (`workerd.capnp:115-116`). Existing tailworks DO data needs a one-time
  importer that writes the bucket layout (a snapshot per object, format in
  `server/cluster/frames.capnp`).

## Q6. Fencing with ADR-028 lease epochs

Workerd has its own fencing, per object, and it is enough for storage
safety on its own:

- `own.json` is "the only authority for who owns it and whose log holds its
  latest commits" (`ownership.rs:21-23`). Each epoch writes its own session
  `log/<epoch>/…`. A takeover restores the last opened session and starts
  the next one from a snapshot, "so a late write from the old owner lands
  where nothing reads it" (`lib.rs:17-20`).
- An ack needs the post-upload re-read of `own.json` to still name the
  writer's epoch (`owner.rs:394-399`). So a fenced host's late frames are
  never acked, and never read.

The ADR-028 lease epoch can't be put into the frames without a patch:
`Frame.session` is workerd's ownership epoch (`frames.capnp`, `session @0`).
The ways to tie the two together:

1. **Two fences in series (recommended to start).** mgmt's lease decides
   which host runs the workload's workerd. The bucket's `own.json` decides
   which instance may ack a commit. The safety of the data rests on the
   bucket alone. The mgmt lease only affects availability. hostd must stop
   workerd when it self-fences: SIGTERM, so the objects are handed off at
   once, rather than left to wait out the 20 s node lease.
2. **hostd as the bucket endpoint.** Point `aws_endpoint` at a loopback S3
   proxy in hostd. It forwards to the real backend, keeps the bucket
   credentials outside the workerd sandbox, and refuses writes once hostd
   has fenced. The lease epoch is checked at hostd, not in the frames. This
   also fits the sandbox: the strict Seatbelt and Landlock profiles limit
   workerd's network to what the workload needs (`hostd/seatbelt.go:20-35`),
   and one loopback port is easier to allow than a remote bucket. Bucket
   credentials can also rotate from a file without a restart
   (`aws_credentials_file`, `workerd.capnp:230-237`).
3. **A patch that carries the mgmt epoch.** Not worth its cost (option B).

Two mismatches to note:

- Workerd's node lease uses wall clocks. Peers judge a lease by their own
  clocks, and an instance more than 2 s off the bucket's clock exits
  (`lease.rs:64-66`, `:87`). ADR-028 chose a single store clock for this
  reason. That's acceptable, because ownership changes are still
  conditional writes, but it is a second clock assumption to document.
- A cluster instance routes requests for objects it doesn't own to the
  owner's advertised address. Use one cluster per workload (a bucket
  prefix per workload, so one member at a time). Bind the cluster address
  to loopback. Then a stale owner is simply unreachable until its lease
  expires.

## Q7. Where frames go, per install shape

Cluster mode writes only to a bucket (`s3://` or `gs://`). There are no
peer followers and no quorum: durability is the bucket's. Cloudflare R2 and
AWS S3 both offer conditional writes (workerd's comments are written
against R2: `lease.rs:23-25`). Any other backend has to pass workerd's
startup check.

| Shape | Destination | RPO for acked writes | Notes |
|---|---|---|---|
| `tw up`, no bucket | Local disk only, as today | Loss of the machine loses everything since the last `tw backup` | Cluster mode can't run without a bucket. Nothing changes. |
| `tw up` with an R2 or S3 bucket configured | The bucket | 0: an ack means the bucket has it | Adds bucket latency (one PUT and one GET per group commit) to every gated DO write. A bucket stall over ~8 s exits workerd (Gaps 1). |
| Multi-host | The bucket the install already needs for R2 blobs (`-r2-backend s3`, ADR-036), under a separate prefix | 0 | Takeover on a new host restores from the bucket, per object. Peer hosts as followers would mean running a distributed S3-compatible store on the hosts. That's possible (the endpoint just has to pass the conditional-write check), but it's operator burden, not a tailworks feature. |

Bytes in the bucket are gzip-compressed Cap'n Proto frames, not encrypted
(`server/cluster/log.rs:261-265`). Custody falls to the bucket's
server-side encryption, as it does for R2 blobs on `s3` today (ADR-036).

## Gaps between cluster mode and tailworks

1. **Process exit on bucket trouble** (Q3). Tailworks runs one workerd per
   workload, with the platform worker, the tenant's stateless Worker and
   its DOs in one process (`hostd/seatbelt.go:153`, `serve <config>
   --experimental`). With cluster mode on, a bucket stall over ~8 s takes
   the stateless Worker down too. That breaks ADR-028's promise that
   stateless Workers keep serving. Fixes: split stateful namespaces into a
   second workerd per workload, or get "degrade instead of exit" upstream
   (ADR-012 already gates v1 on it).
2. **Experimental and fast-moving** (Q1, option A). Each workerd bump needs
   a cluster e2e run, not just the compat suite.
3. **A bucket with conditional writes is required.** There's no story for a
   bucketless `tw up`.
4. **No encryption in the frames** (Q7).
5. **No import of existing local DO storage** (Q5).

The issue's expected wins hold:

- **Replay is exact.** Pages are shipped, not statements.
- **Each write runs once.** The output gate replaces the dry run.
- **Growth is bounded.** Snapshots compact the log (`owner.rs` `tier`, and
  the `SnapshotJustification` `coldStartMitigation` in `frames.capnp`).
- **Plain DOs are protected too.**
- **mgmt is off the data path.** The bucket and hostd are on it instead.

## Recommendation for #38

**Build on this, not on snapshot compaction of the D1 journal.** Use
workerd's cluster mode as the replication layer, rather than writing our
own VFS hook or WAL tailer.

- It already does what SRS does: page capture, a gated ack, epoch fencing,
  per-object restore and log compaction.
- The alternatives are a fork we would have to build (B) or an async
  tailer that can't gate output (D).
- Platform stores (D1 and KV are already DOs in `__tw_store`) then become
  replicated DOs, and mgmt's journal can shrink to leases and metadata.
- Journal compaction would fix only D1's growth. It would leave replay
  drift, double execution and unprotected plain DOs as they are.

Two conditions:

- Ship it **opt-in, for installs with an S3-compatible bucket** (every
  multi-host install already has one for R2 blobs).
- **Don't turn it on for a workload's stateless Workers until Gap 1 is
  solved.**

The short-term #38 fixes (a read-path deadline and serve-stale, and the
docs about mgmt outages) are still needed and are tracked separately.

**First implementation step.** Add a credential-free e2e harness, like the
other `e2e-*` suites. It runs the pinned workerd with a `cluster` config
against a local S3-compatible server that does conditional writes. It uses
one bucket prefix per workload, a loopback cluster address, and
`unsafeTimeDivisor` to shorten waits. It measures four things:

- the latency that a gated DO write adds;
- a hostd kill and restore of one object on a fresh data directory;
- what a bucket pause does to the whole process (Gap 1, measured rather
  than read from source);
- that `__tw_store`'s D1 object restores exactly.

At the same time, open the upstream discussion on "degrade instead of exit"
and a public commit hook (option C). The results decide between splitting
stateful namespaces into a second process and waiting for upstream. Leave
the mgmt journal alone until then.

A prototype in this spike was optional, and wasn't done. Running a local
S3-compatible server for it wasn't available in this session. The answers
above come from source and from the `tw dev` observations.
