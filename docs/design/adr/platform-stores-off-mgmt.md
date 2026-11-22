# ADR-platform-stores-off-mgmt (Proposed): take mgmt off the write path for platform-store data

**Date:** 2026-11-21
**Status:** Proposed. Design only; nothing here is built. For Ross to
review (issue #38). Revises [ADR-044](044-platform-stores.md) §3, §4, §6 and
§8, and the durability parts of [ADR-038](038-d1-databases.md), if
accepted.

**Ross's position (issue #38):** "I do not think the control plane should be
on the write path for tenant data at all. I'm interested in seeing other
options more fully sketched out."

**How to read and amend this doc.** Every claim is numbered: `T.n` for
today's design, `G.n` for goals, `X.n` for shared building blocks, the
option's letter for each option (`A.1`, `A.2`, … for Option A; Option G
uses `HY.n` so it doesn't collide with the goals), `R.n` for what stays on
mgmt, `P.n` for prerequisites, `REC.n` for the recommendation, `Q.n` for
open questions and `V.n` for things to measure before accepting.
A reviewer can cite `B.14` and an amendment can replace one claim without
renumbering the rest: add `B.14a` rather than shifting numbers. Each option
has the same subsections in the same order, so options can be compared
section by section.

Claims with a letter suffix (`B.12a`) and claims numbered past an
option's original last claim were added by the critique rounds recorded
as comments on PR #226 ("Critique round N").

---

## 1. Today (as built at 22301b4)

Code references are to master at 22301b4.

- **T.1 One SQLite DO per store.** Each KV namespace, D1 database and R2
  bucket index is a platform-owned SQLite Durable Object, `Store`,
  addressed `getByName("<kind>:<name>")` in the workload's own workerd
  (`hostd/gen/js/store.js`). Its data lives in the workload's DO storage
  directory (`<data>/tenants/<t>/workloads/<w>/do`; on the microVM tier, the
  per-workload dm-crypt volume, ADR-024).
- **T.2 Journal first, then apply.** `Store.commit` sends entries to hostd
  (`POST /v1/store/append` on the `HOSTD` binding), hostd adds tenant,
  workload and lease epoch and calls mgmt (`POST …/stores/{kind}/{name}/journal`),
  and only after mgmt answers does the object apply them in one
  `transactionSync` (`store.js` `commit`). The append is conditional on
  `after` (the head the object has applied) and the incarnation; a loser
  gets `409 store_conflict`, catches up and retries (up to 8 times).
- **T.3 D1 executes every write twice.** D1 runs a write in a rolled-back
  transaction first (the dry run: errors, limits, "did it write"), journals
  the statements and parameters, then applies them
  (`store-d1.js` `write`). The dry run exists because a local apply that
  is already committed cannot be undone if the append then fails.
- **T.4 The seam is hostd's local store API.** `store.js` knows only
  `read`, `append` and `status` on hostd (`hostd/stores.go`). Nothing in the
  object names mgmt. hostd checks the store is declared by the running
  deployment and adds identity and epoch; mgmt is reached through
  `hostd/mgmtapi` with a 30 s client timeout (`hostd/mgmtapi/client.go:97`).
- **T.5 Fencing is mgmt, per call.** `mgmt.fenceStore` admits a host only if
  it holds the workload's lease at the epoch it names and the lease has not
  expired (`mgmt/stores.go`). It runs before, not inside, the append
  transaction, so it is a check-then-act. That is safe only because the
  conditional append (T.2) is what keeps the journal linear: **consistency
  comes from the compare-and-append; the lease check is access control.**
  Replicas (`<w>--r<n>`) already write one journal concurrently under
  different leases, kept consistent only by the conditional append.
- **T.6 mgmt seals; mgmt can read.** Hosts send plaintext entries to mgmt.
  mgmt seals each with AES-256-GCM under a per-store data key held in
  custody (`tw/<tenant>/stores/<incarnation>/key`), AAD = incarnation and
  seq, and stores an HMAC of the `ckey` (`sealStoreEntry`, `storeCKey`).
  So the database and its backups hold ciphertext, but **the mgmt process
  sees every entry in plaintext** on append and on read, and holds the key.
  Admin KV reads (`tw kv key get/list`) fold the plaintext journal inside
  mgmt (`mgmt/kv.go` `kvFold`). "mgmt can't read the entries" (issue #38's
  wording) is true of mgmt's database at rest, not of mgmt.
- **T.7 Recovery is replay.** A store object with no local copy (a move, a
  forced takeover, a lost disk, `--replace`) replays the journal from seq 0.
  Catch-up also runs after a conflict, and on a read whose last sync is
  older than the kind's freshness: D1 1 s (`READ_FRESH_MS`), KV 60 s by
  default (`DEFAULT_FRESH_MS`, or the read's `cacheTtl`); R2 reads catch up
  first.
- **T.8 Compaction is by `ckey` only.** KV entries replace each other by
  key, so a KV journal stays the size of the live data, except tombstones
  (deleted keys never written again). R2 index rows likewise. D1 entries
  have no `ckey`: the journal keeps every write, up to 1 GiB per store
  (`StoreMaxBytes`).
- **T.9 Replay is not exact for D1.** Statement replay re-evaluates
  `random()`, `randomblob()`, `datetime('now')` and `DEFAULT
  CURRENT_TIMESTAMP` (measured: `d1_migrations.applied_at` changes after a
  rebuild). An entry that fails on replay stops the replay and degrades the
  store (#106, #147); `tw d1 repair --skip <seq>` deletes it from the
  journal.
- **T.9a Replicas diverge today, not only on a rebuild.** Every replica
  of a workload (`<w>--r<n>`) keeps its own copy of a D1 database (ADR-038
  §"read replicas": "each of which has its own copy") and fills it by
  catch-up, which is statement replay. So the same non-deterministic
  statements give each replica different values (`random()`,
  `datetime('now')`, `DEFAULT CURRENT_TIMESTAMP`) the moment they are
  applied. A later deterministic statement can then do different things
  on different replicas, or fail on one and degrade it. The *journal* is
  linearizable (T.5), but the materialized copies are not identical.
  ADR-038 records re-evaluation only "when a host rebuilds a database".
- **T.10 mgmt is on the read path too** (issue #38 comment). A read whose
  freshness lapsed waits on a sync that is chained behind every earlier
  commit or sync on the object (`#locked`), with no coalescing and no
  deadline but hostd's 30 s client timeout. A slow or black-holed mgmt
  stalls D1 reads for up to 30 s per queued sync. A cleanly-down mgmt fails
  fast and the object serves its local copy (fail static).
- **T.11 The 30 s fence stops stores anyway for workloads with DO classes.**
  hostd undeploys every workload with a fenceable tenant DO class after
  `FenceAfter` (30 s, ≤ 40 s) without a successful heartbeat (ADR-028 §3),
  which stops its platform `Store` objects too. A KV/D1/R2-only Worker has
  no fenceable class (`mgmt.BundleStateful` counts tenant classes) and keeps
  serving; its stores are fenced only per call by mgmt (T.5).
- **T.12 R2 bytes are already off mgmt.** R2 object bytes go to an
  operator-configured blob backend that hostd talks to (`fs` or any
  S3-compatible service, `hostd/r2`, ADR-036 §2). Only the R2 *index* is a
  journaled store. A multi-host install that uses R2 already needs a shared
  S3-compatible store reachable from every host.
- **T.13 Queues are a broker in mgmt** (ADR-039), not a platform store:
  messages are sealed rows in mgmt's database. Every send, claim and ack is
  an mgmt call.
- **T.14 Limits that matter for recovery.** `tw d1 export` refuses output
  over 16 MiB (`resultBytes` in `store-d1.js`) and `tw d1 execute --file`
  refuses bodies over 8 MiB (`d1BodyLimit`, `mgmt/d1.go`) and writes over
  2 MiB encoded. So export and import cannot rebuild a large database today.
- **T.15 Tenant DOs and Workflows have no off-host copy at all.** Their
  storage is the host-local directory; a move starts them empty
  (`tw host drain --allow-local-state-loss`). Workflows (ADR-040) keep
  instance state in DO storage too. The RFC's async encrypted backup
  (§4.9, §10) is not built.
- **T.16 R2 blob bytes are not sealed by tailworks.** The R2 blob backend
  stores object bytes as the Worker wrote them; with `s3`, encryption at
  rest is the object store's (ADR-036, "encrypted by tailworks" only for
  the journaled index). So the object store operator already holds R2
  plaintext today.
- **T.17 A mgmt restore already fences epochs.** `tw-mgmt restore-fence`
  raises every lease epoch by `RestoreEpochJump` (2^20) after a restore
  (`mgmt/sqlstore_upgrades.go` `RestoreFence`), so no epoch handed out
  before a restore is reused after it. Lease rows are kept on release
  (`ReleaseLease`) and deleted only with their tenant, so a lease's epoch
  is monotonic for the life of the tenant.
- **T.18 Hosts measure time with Go's monotonic clock.** The ADR-028 fence
  is `time.Since(h.lastHB) > FenceAfter` (`hostd/host.go`). Go documents
  that "on some systems the monotonic clock will stop if the computer goes
  to sleep"; in practice it excludes suspend on Linux (`CLOCK_MONOTONIC`)
  and macOS. A host that sleeps (a laptop on the process tier, a paused VM)
  wakes believing almost no time passed. Every duration-based guard in
  this doc (X.7, A.6, B.12) inherits that. (Go's runtime reads
  `CLOCK_MONOTONIC` on Linux and `mach_absolute_time` on macOS
  (`runtime/sys_darwin_*.s`); neither advances during sleep.)
- **T.19 mgmt's journal has gaps.** `ckey` compaction deletes superseded
  rows in the append transaction (`mgmt/sqlstore_stores.go`
  `AppendStore`), so a KV or R2-index journal's seqs are sparse and its
  `head` can be past its last row; the store object then jumps its `seq`
  to `head` (`store.js` `#catchUp`). D1 journals have no gaps.
- **T.20 An in-transaction fence exists but stores don't use it.**
  `checkFence` (`mgmt/sqlstore.go`) checks the lease inside a write
  transaction for other fenced writes; `AppendStore` checks the lease in
  `fenceStore` before its transaction (T.5). Moving the check inside
  would make today's store fence atomic, which matters only if mgmt stays
  on the path.

## 2. Goals, non-goals and constraints

- **G.1** No per-write call to mgmt for KV, D1 or R2-index writes, and no
  per-read call either (T.10).
- **G.2** No tenant data in mgmt's database, and ideally none passing
  through the mgmt process (T.6). mgmt keeps metadata: which stores exist,
  their incarnations, keys in custody, leases and epochs, degraded records,
  audit.
- **G.3** Keep what ADR-044 got right: an acknowledged write survives a
  host loss (RPO 0) where we promise it; two hosts never diverge a store;
  data is sealed at rest outside the host; delete crypto-shreds.
- **G.3a Clarification (round 3).** "Two hosts never diverge a store"
  holds for the journal (the order and content of entries). It doesn't
  hold for D1's materialized copies on replicas (T.9a), today or under
  A–D. Only exact replay (F, or E for one host) or a single copy per
  store gives it.
- **G.4** No reads of workerd's private files unless the option says so
  and pays for it (ADR-012, RFC §14 P3 "No reads of private workerd
  files").
- **G.5** Works on both tiers (process, microVM) and on a single-host `tw
  up` homelab without new infrastructure there.
- **G.6 Non-goal:** global read replicas, point-in-time restore of
  arbitrary times, sharing stores between workloads. None of the options
  block them.
- **G.7 Non-goal:** Queues (T.13). §5 says why they are a separate design.
- **G.8 Constraint:** mgmt stays the authority for leases and epochs
  (ADR-011, ADR-028). Every option below is judged on whether it needs mgmt
  *per write*, *per lease change*, or *not at all*. "Off the write path"
  in this doc means per lease change at most.

## 3. Shared building blocks

The options reuse a few mechanisms. They are described once here.

### X1. The seam stays where it is

- **X.1** `store.js` talks only to hostd (T.4). Options A–D change what
  hostd does behind `/v1/store/read|append|status`, not the object's
  contract (`after`, `incarnation`, `head`, `store_conflict`). That keeps
  the kinds (KV, D1, R2) and their tests unchanged, and keeps backend
  credentials out of workerd (ADR-034), as the R2 blob backend already
  does (T.12).
- **X.2** On the microVM tier the Store object runs in the guest and
  reaches hostd over the guest NIC (ADR-044). Moving the backend behind
  hostd changes nothing in the guest.

### X2. Host-side sealing with a key released at the lease

- **X.3** To keep plaintext out of mgmt (G.2), the host must seal. mgmt
  releases the store's data key to the lease holder at its epoch, as it
  already releases secrets and volume keys (`VolumeKeyGrant`, "a fenced
  release of a volume's data key"). One release per (store, epoch) on
  first use; hostd caches it in memory only. mgmt is then on the path
  once per lease change, not per write.
- **X.4** The seal format can stay byte-for-byte what mgmt uses today
  (AES-256-GCM under `subKey(key,"seal")`, AAD `tw-store-v1\n<inc>\n<seq>`),
  so existing sealed rows can be copied without re-sealing during
  migration.
- **X.5 What changes in exposure.** Today a host holds the plaintext of
  its stores in DO storage (unencrypted by tailworks on the process tier)
  but never the key. With X.3 it also holds the key while it holds the
  lease, so it can decrypt the *whole history* in the backend, including
  entries a snapshot has superseded but GC has not yet deleted. A host that
  lost the lease keeps the key in memory until it restarts. This is a real
  widening, small next to what a host already sees.
- **X.6 Optional: a key per epoch.** mgmt could mint a fresh subkey per
  epoch and release `K_1..K_E` to the holder at `E`: a host that lost the
  lease at `E` cannot read entries written at `E+1` or later. Snapshots
  re-seal under the newest key, after which older keys can be destroyed.
  Cost: a key list per store in custody and more release logic. Open
  question Q.6.
- **X.16 Optional: backend credentials released at the lease too.**
  Today every host holds the operator's object-store credentials for the
  whole bucket (T.12). Under B that lets any host, including one whose
  process-tier sandbox a tenant escaped, list every tenant's store names,
  sizes and write timing, and delete any tenant's log. Where the backend
  supports it, mgmt can mint short-lived credentials scoped to the
  store's prefix at each lease grant (AWS STS with a session policy; R2
  and MinIO both have temporary, prefix-scoped credentials), so a host
  can touch only the stores it currently leases. Same cadence as X.3 (per
  lease change), so it doesn't put mgmt on the write path, but mgmt then
  holds a credential that can mint credentials for the bucket. Garage and
  `fs` have nothing like it.

### X3. Fencing without mgmt per write

Three mechanisms, used differently by the options.

- **X.7 Time-bounded lease (what ADR-028 does).** The host stops when its
  last successful heartbeat is older than `FenceAfter` (< lease TTL). It
  needs mgmt to renew. Applied to store writes, it caps write availability
  during an mgmt outage at `FenceAfter`, because a host cannot tell "mgmt is
  down" from "I am partitioned and mgmt gave my lease away".
- **X.8 Epoch token checked by the store.** The writer presents
  `(writer, epoch)`; the storage service remembers the highest epoch it has
  seen per (store, writer) and refuses anything lower. Classic fencing
  token. Needs a storage service that can run that check (a database, a
  peer host), not a plain object store.
- **X.9 Barrier entry in a compare-and-append log.** When a host first uses
  a store at a new epoch, before it serves a read or a write, it catches up
  and appends a substrate-only **barrier** entry `{writer, epoch}` at
  `head+1`, conditionally. Every later append is conditional on the
  writer's own head. Then:
  - a stale writer (lower epoch, same writer identity) whose head is
    before the barrier can't append at any seq up to the barrier (those
    slots are taken), and its first conditional append past them fails;
    its catch-up then reads the barrier and it fences itself
    (`lease_lost`), for reads and writes;
  - writes the stale holder made *before* the barrier are ordinary log
    entries, replayed by the new holder: acknowledged writes are never lost;
  - so **the log itself is the fencing arbiter**, and no lease freshness is
    needed per write. This is how replicated logs use leader epochs.
  - Writer identity is the lease name (`<w>` or `<w>--r<n>`), so replicas
    keep writing one journal concurrently as they do today (T.5).
- **X.9a The barrier must be authenticated.** A barrier is sealed under
  the store key like any entry (AAD naming it a barrier, the writer and
  the epoch). An unsealed barrier lets anyone with backend write access
  append `{writer, epoch: 2^62}` and fence the real holder out for good.
  A holder that reads a barrier for its own writer at an epoch *higher*
  than its own fences itself; one that reads a barrier at a *lower* epoch
  ignores it. Epochs never go backwards, including across a mgmt restore
  (T.17).
- **X.10** X.9 requires the backend to offer an atomic "create if absent"
  for a log slot and read-after-write consistency for it. A plain
  conditional PUT (`If-None-Match: *`) on an S3-compatible store, or
  `link(2)`/`O_EXCL` plus fsync on a local filesystem, gives that. Support
  differs by S3 implementation and version: AWS S3 and Cloudflare R2
  document conditional PUT; MinIO, Garage and SeaweedFS must be checked per
  version (V.2). hostd should run a probe at start (create a slot twice;
  the second must fail) and refuse a backend that passes both.
- **X.10a What we know per backend** (to be confirmed by V.2):
  - *AWS S3*: `If-None-Match: *` on PutObject since August 2024; S3 is
    strongly consistent for reads and LIST. Concurrent conditional writes
    can answer `409 ConditionalRequestConflict` as well as `412`, and a
    retry after a lost response can answer `412` for the caller's own
    write. hostd treats both as "conflict, then check the append ID"
    (B.28).
  - *Cloudflare R2*: documents `If-None-Match: *` on PutObject as checked
    atomically against committed state; one racer wins, the rest get 412.
  - *MinIO*: documents `If-None-Match` on PutObject. The community edition
    has been source-only and in maintenance mode since late 2025, so
    "MinIO" as a recommended homelab backend is itself a risk.
  - *Garage*: its documented design reads and writes by quorum with no
    primary and reconciles with CRDT rules; it documents no conditional
    PUT. A create-if-absent that is atomic between two concurrent writers
    needs a single serialization point, which that design doesn't have.
    Treat Garage as **unable to back B** unless V.2 proves otherwise.
    ADR-036 lists Garage for R2 blobs, which need no conditional writes;
    an operator who chose it for R2 can't reuse it for B.
  - *Any multi-site or asynchronously replicated deployment* (MinIO site
    replication, Ceph multisite, a bucket replicated across regions with
    writers in both): a conditional create is atomic per site at most.
    Every host must write to the same site.
  - *`fs`*: atomic `link(2)` (or `O_EXCL`) plus fsync of the file and its
    directory on a local filesystem. Today's `fs` R2 backend uses
    `CreateTemp` + `rename`, which *replaces* (`hostd/r2/fs.go`), so the
    journal needs a new primitive. NFS is out: `O_EXCL` isn't reliable on
    older NFS, and `fs` is single-host by definition.
- **X.10b The probe can't prove atomicity.** A gateway that emulates
  `If-None-Match` with a HEAD then a PUT passes a sequential probe and
  loses races. The probe should race N concurrent creates of one slot and
  require exactly one success, and still counts only as a smoke test; the
  supported list (V.2) is what we stand behind.
- **X.11 Stale reads.** A fenced-off holder can keep serving reads from its
  local copy until its next catch-up sees the barrier. That window is the
  kind's freshness (D1 1 s, KV `cacheTtl`, R2 every read), the same bound
  as today, now enforced against the backend rather than mgmt.
- **X.11a Correction: under a partition the stale-read window is
  unbounded, today and under every option.** `fresh()` serves the local
  copy on any catch-up error except `lease_lost` (`store.js` `fresh`). A
  holder cut off from mgmt (today) or from the backend (B) never sees the
  barrier or the `lease_lost`, so after a takeover it keeps answering reads
  from its old copy for as long as clients can reach it. KV/D1/R2-only
  Workers have no ADR-028 fence to stop them (T.11). P.1 (serve stale on
  a failed sync) makes this the common path rather than a corner. The
  freshness bound in X.11 holds only while the holder can reach its
  journal. A cap is possible: refuse reads (`store_unavailable`) once the
  last successful catch-up is older than `maxStale`, measured on a clock
  that counts suspend (T.18). It trades availability for a staleness bound
  (Q.11).

### X4. Snapshots by the Store object

- **X.12** A snapshot is a logical dump the Store object makes of its own
  tables at a seq, through the public SQL API: KV and R2-index rows; for D1
  the tenant's schema, rows (as literal `INSERT`s), `sqlite_sequence`,
  views and triggers, as `tw d1 export` does. No workerd private files.
- **X.13** Restoring a snapshot is exact (literal values, no
  re-evaluation), so replay drift (T.9) is limited to entries after the
  newest snapshot.
- **X.14 Cost: consistency means holding writes.** A DO has no read
  transaction that spans `await`s, so a consistent dump of a large
  database must hold the store's write chain (`#locked`) while it reads in
  pages; reads interleave, writes queue. For a 1 GiB D1 database that is
  on the order of seconds (to measure, V.4). Today's export is capped at
  16 MiB (T.14) and must become streaming and chunked first.
- **X.14a** "Seconds" assumes the dump is read into memory under the lock
  and uploaded after it. A dump streamed straight to the backend while
  holding the lock stalls writes for the upload too, which on a remote
  object store is tens of seconds for 1 GiB. The choice is memory (a full
  copy in the isolate) or a longer stall; a third way is to dump to a
  scratch table or local file first, which doubles local disk use.
- **X.15** Snapshots are sealed in chunks (same seal, AAD naming the
  snapshot and chunk) and written as one manifest plus chunks; the
  manifest is written last, so a partial snapshot is never used.

---

## 4. Options

Each option has the same subsections: summary, write path, durability and
mgmt-down availability, fencing, custody, move/takeover/host loss,
compaction and growth, replay exactness, operator burden, workerd file
layout, migration, failure modes, verdict.

### Option A: host-local stores plus periodic snapshots to an object store

**Summary.**

- **A.1** No journal. A store's DO storage is the source of truth. The
  Store object writes a snapshot (X.12) to an object store every `I`
  seconds while it has changes, and on a planned move. This is ADR-044's
  rejected option 3, done at the Store level with public APIs instead of
  shipping workerd files.

**Write path and latency.**

- **A.2** Steps: (1) apply in one `transactionSync`; (2) answer. Local
  SQLite only. D1 executes each write once (the dry run of T.3 goes away).
- **A.3** Latency is a local SQLite commit. Snapshot uploads are
  asynchronous, but a snapshot holds writes while it dumps (X.14), so a
  large store sees periodic write stalls.

**Durability, RPO and mgmt-down availability.**

- **A.4** RPO on host loss is up to `I` plus the upload time. RPO on a
  planned move is 0 (final snapshot before release, A.10).
- **A.5** Reads and writes need neither mgmt nor the object store. A
  failed upload only grows the RPO window; it should raise a metric and
  mark the store degraded after a bound.

**Fencing.**

- **A.6** Snapshots alone can't be compare-and-appended per write, so
  writes need a time bound. The new holder creates a barrier object
  `epoch/<E+1>` (conditional create) and then waits `W` before it accepts
  writes; every holder polls for a higher `epoch/` object every `W/3` and
  refuses writes when its last successful poll is older than `W - margin`
  (monotonic clock). This is the RFC's original object-store lease
  (§4.9). It depends on the object store, not mgmt, but has the same
  clock-rate assumptions as ADR-028.
- **A.7** Snapshot objects are named `snap/<epoch>/<gen>`; a restore uses
  the highest complete `(epoch, gen)`. A stale holder's later snapshots at
  a lower epoch are ignored.

**Custody and encryption.**

- **A.8** hostd seals snapshots with the store key released at the lease
  (X.3). The object store holds ciphertext; its operator sees sizes, names
  and timing. Delete crypto-shreds as today.

**Move, takeover, host loss.**

- **A.9** Host loss or forced takeover: the new holder restores the newest
  snapshot. Writes since then are lost; if the old disk comes back they
  are stranded there (the epoch rule ignores them). An export-from-old-disk
  tool would be the only way to recover them.
- **A.10** Planned move (`--replace`, drain): the old holder takes a final
  snapshot at its epoch and only then releases. That needs a "release with
  snapshot" step in the move protocol (mgmt waits for the old host's
  report or times out to the takeover path).

**Compaction and growth.**

- **A.11** Nothing grows except snapshots: keep the newest two complete
  snapshots per store and delete older ones. mgmt's database holds no
  data.

**Replay exactness.**

- **A.12** Exact: there is no replay, only snapshot restore (X.13).

**Operator burden and new components.**

- **A.13** An S3-compatible store for multi-host (the same one R2 uses);
  the `fs` backend for single-host `tw up`. No new service.

**workerd file layout.**

- **A.14** None used (X.12).

**Migration from today's journal.**

- **A.15** Per store, the lease holder takes a first snapshot, then mgmt
  flips the store to `backend=snapshot` and refuses further appends. Same
  shape as B.25 (cross-reference corrected in round 2; it read B.21).

**Failure modes.**

- **A.16** Host loss loses up to `I` of acknowledged writes, silently from
  the application's point of view. Cloudflare's KV and D1 don't do that;
  apps written for them assume an acknowledged write is durable.
- **A.17** Object store down for longer than `W`: writes stop (A.6), reads
  continue. Snapshot upload failures widen RPO without any error to the
  caller.
- **A.18** Snapshot stalls (X.14) become visible latency on big D1
  databases at every interval, not only at compaction time.

**Verdict.**

- **A.19** Cheapest and fastest, and honest only as an opt-in for data an
  app can afford to lose (caches, derived data, write-heavy counters). Not
  acceptable as the default for D1 or KV.

### Option B: a fenced journal in an object store; mgmt issues only epochs and keys

**Summary.**

- **B.1** Keep ADR-044's model (journal first, local SQLite as a cache of a
  prefix of the journal), but the journal lives in an object store that
  hostd writes directly: one immutable object per append, named by its
  first seq, created conditionally (X.10). Fencing is the barrier rule
  (X.9). The host seals (X.3). Snapshots (X.12) compact the log. mgmt is
  consulted per lease change (epoch, key release), never per write or read.

**Object layout.** (Under the operator's prefix, next to R2 blobs.)

- **B.2** `stores/t/<tenant>/w/<workload>/<kind>/<name>/<incarnation>/`:
  - `log/<seq, 20 digits>`: one sealed append (1 to 128 entries, seqs
    `first..last` inside), created with `If-None-Match: *`;
  - `snap/<seq>/manifest` and `snap/<seq>/<chunk>`: a snapshot at `seq`,
    manifest last (X.15);
  - nothing mutable. There is no head pointer: a reader lists `log/` after
    the newest snapshot (`start-after`), and a freshness check is one GET
    of `log/<seq+1>` (404 means up to date).
- **B.2a Invariant: the log is gap-free.** Each log object starts at the
  seq after the previous object's last. The freshness check (404 on
  `log/<head+1>` means "up to date") and the conditional append both rely
  on it: with a gap at `head+1`, a writer would think itself current and
  its create at `head+1` would *succeed*, ordering its write before
  entries it never saw. This rules out copying mgmt's sparse journal
  (T.19) object by object (B.25a). Precisely: gap-free *after the newest
  snapshot*. GC creates a leading gap by design, and B.12d covers how
  readers detect it.
- **B.3** Tenant and workload names appear in object names, as they do for
  R2 blobs today (ADR-036 §4). KV keys and SQL never do. HMAC'd path
  elements are possible but make operator debugging harder (Q.8).

**Write path and latency.**

- **B.4** Steps, unchanged in `store.js`: (1) D1 dry run (T.3); (2)
  `POST /v1/store/append {after, incarnation, entries}` to hostd; (3) hostd
  seals the entries (seq `after+1…`) into one object and PUTs
  `log/<after+1>` with `If-None-Match: *`; (4) on 412 hostd answers
  `store_conflict` and the object catches up (LIST + GETs) and retries, as
  today; (5) on success, apply and answer.
- **B.5** Latency: one conditional PUT to the object store instead of a
  tailnet round trip to mgmt plus a SQL transaction. On a LAN MinIO that
  is typically a few milliseconds; on a remote cloud object store it is
  typically tens of milliseconds, and may be *slower* than today's mgmt
  append. Not measured here (V.1). The object store's PUT latency becomes
  the D1 write floor.
- **B.6** Concurrent writes already serialize on the object's `#locked`
  chain (T.10). Batching queued commits into one append (group commit, up
  to 128 entries, which the API already allows) amortizes PUTs under load.
  That is a `store.js` change and also helps today's design.
- **B.7** Request-priced object stores charge per PUT, GET and LIST. A
  write-heavy store on a cloud object store costs money per write; the D1
  1 s freshness check costs a GET per second per active store.

**Durability, RPO and mgmt-down availability.**

- **B.8** RPO 0 for host loss and moves: a write is acknowledged only after
  the object store accepted its log object. Durability is the object
  store's (versioning and replication are the operator's choice).
- **B.9** mgmt down: reads and writes continue for as long as the holder
  keeps its lease locally and the object store is up, for any length of
  outage (unless hostd restarts, B.29a), because nobody can take the
  lease over while mgmt is down and the barrier protects against a
  takeover while mgmt is up but the host is partitioned.
  **Except** workloads with fenceable tenant DO classes: the ADR-028 fence
  still undeploys them after `FenceAfter` (T.11), because their tenant DO
  storage has no such protection. B fixes availability for
  KV/D1/R2-only workloads and for the first 30 s of an outage for the rest.
- **B.10** Object store down: writes fail (`store_unavailable`); reads fail
  static from the local copy, as reads do today when mgmt is down. The
  object store replaces mgmt as the store's single dependency; it does not
  remove the dependency.

**Fencing.**

- **B.11** The barrier rule (X.9), with writer identity = lease name.
  hostd writes the barrier on the first read or write of a store at a new
  epoch, before answering it. The kinds never see barrier entries.
- **B.12 The GC hazard and its fix.** If GC deletes `log/<k>` after a
  snapshot, a stale writer whose head is `k-1` can *create* `log/<k>`
  again (create-if-absent succeeds on a deleted key), and readers, which
  start after the snapshot, never see it: an acknowledged write silently
  lost. Fix, both parts needed:
  - GC replaces superseded log objects with small tombstones rather than
    deleting them, and deletes tombstones only after `T_gc`;
  - a writer refuses to append when its last successful catch-up is older
    than `T_stale < T_gc` (monotonic clock), and a catch-up that lands on
    a tombstone loads the snapshot instead.
  `T_gc` can be hours, so this is a clock assumption with a wide margin,
  not ADR-028's tight one.
- **B.12a Correction: the `T_stale` guard fails across a suspend.** A
  monotonic `T_stale` doesn't advance while the host sleeps (T.18). A
  laptop host closed overnight wakes with its last catch-up "a minute
  ago", the tombstones it would have hit are gone, its create-if-absent
  on a deleted slot succeeds, and the write is acknowledged and then
  invisible to every reader: silent loss of an acknowledged write. A wider
  margin doesn't fix it; the clock is wrong, not tight.
- **B.12b A clock-free fix.** After a successful PUT of `log/<k>` and
  before acknowledging, hostd checks that no complete snapshot covers `k`
  (one LIST of `snap/` with `start-after` its head, or a GET of the next
  expected manifest). This is sound on a strongly consistent backend: GC
  deletes `log/<k>` only after a snapshot covering `k` is complete, and
  that deletion happened before the stale PUT succeeded, so a read after
  the PUT sees the snapshot. If one exists, hostd answers `store_conflict`
  instead of success; the orphan object is ignored by readers and removed
  by the next GC. Cost: one extra request per write, roughly doubling
  write latency, so the practical choice is between B.12b on every append
  and B.12 with a clock that counts suspend (`CLOCK_BOOTTIME` on Linux;
  wall-clock checked alongside the monotonic one elsewhere) plus B.12b
  only when the writer's last catch-up is older than a few seconds.
  Either way, no lease timing is involved.
- **B.12c Simplification.** If B.12b runs on every append, a reborn slot
  is always caught by its writer, so GC can delete covered log objects
  outright: no tombstones, no `T_gc`, no clock. Readers already start
  after the snapshot. Tombstones are needed only in the variant that
  skips B.12b for recently caught-up writers.
- **B.12d Correction to B.12c (round 3): tombstones are still needed for
  reads.** B.12b protects *appends* only. The freshness check is a GET of
  `log/<head+1>`. For a holder whose head is behind a snapshot whose
  covered log GC deleted outright, that GET answers 404, and the holder
  concludes it is up to date. It then serves an arbitrarily old copy, with
  no partition and no error, until its next write. Either keep tombstones
  (B.12) so the GET finds one and the holder loads the snapshot, or make
  the freshness check also look for a newer snapshot, which costs two
  requests per check. In both cases a catch-up whose LIST starts after
  `head` and returns a first object that doesn't begin at `head+1` has
  found a GC gap, and loads the snapshot. Keeping tombstones is the
  cheaper choice. Without B.12b, though, tombstones bring back `T_gc` and
  the clock, so the full fix is tombstones *and* B.12b, with tombstones
  deleted by age only as a storage optimization.
- **B.13** mgmt still controls who may hold the lease, hence who gets the
  key (X.3) and who may write the barrier. A host that never held the
  lease has no key and cannot produce entries that open. A host that holds
  object-store credentials but not the key can damage (write garbage,
  delete) but not read or forge; a garbage `log/<k>` is a denial of
  service that stops replay at `k` (degraded, as today with a corrupt
  entry). The object-store credentials are operator credentials on every
  host, as for R2 today.
- **B.13a The log has no authenticated head, so a lost tail is
  invisible.** Today mgmt's `head` is the authority: a host that gets
  fewer entries than `head` knows. Under B (B.2, "nothing mutable"), if the
  newest log objects disappear (a delete by a host with bucket
  credentials, an operator lifecycle rule on the shared bucket, a bucket
  restored from an asynchronous replica or an older backup), the next
  holder replays a shorter log and serves it as complete. Acknowledged
  writes are lost with no error and no degraded record. That undercuts
  B.8's RPO 0, which in fact rests on the backend never losing a
  committed object.
- **B.13b Detection without the write path.** mgmt records a high-water
  mark per store: `(head, hash of the last log object)` at every lease
  change (the outgoing holder's last known head, reported with its
  release or the new holder's barrier) and asynchronously from heartbeats
  (bounded staleness, off the write path; G.8 allows per lease change).
  Each log object also carries the hash of its predecessor, so a
  substituted or reordered object breaks the chain. A holder whose
  recovered log ends before mgmt's mark, or whose chain breaks, degrades
  the store and refuses writes instead of serving it. This detects a lost
  tail older than the last report; it doesn't prevent loss, and a tail
  lost within one report interval is still silent. Object lock or
  versioning on the journal prefix (B.31) is the prevention.

**Custody and encryption.**

- **B.14** The store data key stays in mgmt custody; mgmt releases it to
  the lease holder at its epoch (X.3). hostd seals and opens; mgmt never
  sees an entry (G.2). The object store operator sees ciphertext, object
  names (B.3), sizes and timing.
- **B.14a** The object store operator sees ciphertext for the journal
  and snapshots only; R2 blob bytes in the same bucket are plaintext today
  (T.16). If G.3's "sealed at rest outside the host" is meant for R2 too,
  hostd can seal blobs with the same lease-released key (X.3), as a
  separate change.
- **B.14b** Every host, not only the holder, can see every tenant's store
  names, sizes and write timing in the bucket unless X.16 scopes
  credentials. Today a host sees only its own workloads' stores. This is a
  cross-tenant metadata leak to hosts that B introduces for stores (R2
  blobs already have it).
- **B.15** Who can read tenant data: the current lease holder (it already
  can), a former holder until it restarts (X.5; X.6 narrows it), mgmt (it
  can release itself the key; custody is mgmt's, as for secrets), and
  nobody with only the object store or only mgmt's database. Delete still
  crypto-shreds: deleting the custody key makes every log object and
  snapshot unreadable, before GC removes them.
- **B.15a Who deletes a deleted store's objects?** Nothing in B does.
  GC runs in the lease holder of a *live* store; mgmt has no backend
  credentials (Q.4); ADR-036 already leaves R2 blobs of a deleted workload
  or tenant behind ("Open: … deleting a workload's blobs when it is
  deleted"). Under B every deleted store, workload and tenant leaves its
  ciphertext, names and sizes in the bucket forever. Because B's prefixes
  include the incarnation, a whole-prefix delete is safe (no new
  incarnation writes there). So mgmt should queue a "delete prefix" task
  on store, workload and tenant delete and hand it to any host with
  backend access through the existing assignment channel, retried until
  done: control-plane work, off the write path. Until it runs, a host
  that still has the old key in memory (X.5) can read the old log.

**Move, takeover, host loss.**

- **B.16** All three are the same path, as today: the new holder gets the
  lease and key from mgmt, loads the newest snapshot, replays the log
  after it, writes its barrier, then serves. Time to first request is
  snapshot download plus tail replay instead of a full replay, so large D1
  databases recover faster than today once snapshots exist.
- **B.17** A forced takeover while the old host is alive but partitioned
  is safe (X.9): the old host's writes before the barrier survive, after
  it they fail.

**Compaction and growth.**

- **B.18** The lease holder takes a snapshot when the log since the last
  one exceeds a size or count threshold (and never more often than a floor
  interval), then tombstones the covered log objects (B.12). It keeps two
  complete snapshots. KV's `ckey` compaction (T.8) becomes snapshot
  compaction; tombstones of deleted KV keys disappear at the next snapshot,
  closing ADR-044's open tombstone item.
- **B.18a Replicas need one snapshotter and a lag allowance.** With
  replicas (`<w>--r<n>`), only the primary lease holder (`<w>`) snapshots
  and GCs; two writers snapshotting the same store race on GC. A replica
  whose head falls behind a snapshot whose covered log was already
  tombstoned must reload the whole snapshot (B.12). For a large D1 behind
  a 60 s KV-style freshness, that is a full download per lag event. GC
  should keep the log after a snapshot for at least the longest replica
  freshness window plus a margin, so ordinary lag catches up from the
  log.
- **B.19** Growth is in the object store, bounded by live data times two
  plus the log tail. mgmt's database holds no tenant data, so the 1 GiB
  per-store limit becomes a product choice rather than a protection for
  mgmt (Q.9).

**Replay exactness.**

- **B.20** Exact up to the newest snapshot (X.13); statement replay (T.9)
  only for the tail after it. A rebuild re-evaluates at most the
  non-determinism of the last interval, not of the database's whole life.
  Degraded-entry handling (#106) is unchanged. `tw d1 repair --skip` can no
  longer delete a log entry (that reopens B.12); it appends a substrate
  `skip <seq>` marker instead, and replay looks ahead for markers.
  (Looking ahead means replay reads the whole tail before applying
  anything after a failing entry, and markers must be sealed like
  barriers, X.9a, or any host with bucket access can skip entries.)

**Operator burden and new components.**

- **B.21** No new service. hostd needs a journal backend: the R2 backend
  extended (`hostd/r2` has `Put/Get/Delete`; it needs `PutIfAbsent` and
  `List`) or the same flags under a new name. Multi-host installs must
  configure an S3-compatible store, which those using R2 already do (T.12);
  those that don't, now must. Single-host `tw up` uses `fs` under the state
  dir, which is the same disk mgmt's database is on today: no loss of
  durability there.
- **B.22** Every host that may run a workload must point at the same
  backend. hostd reports its backend fingerprint to mgmt; placement
  refuses a host with a different one for a workload whose stores are
  object-backed.
- **B.23** Operators back up the object store instead of (for this data)
  mgmt's database. Restoring mgmt from backup no longer rolls tenant data
  back with it, which is a behaviour change worth documenting.
- **B.23a A mgmt restore and the object log diverge.** Epochs are safe
  (T.17), but store metadata isn't. A store created after the backup has
  a log in the bucket that the restored mgmt doesn't know; the workload's
  next first append creates a *new* incarnation (`AppendStore` with
  `after 0` creates the store) and the old data is stranded under the old
  prefix, silently. A store deleted after the backup comes back as a row
  whose key was already shredded. `restore-fence` must reconcile: list
  incarnation prefixes in the backend, compare with mgmt, and hold any
  `(tenant, workload, kind, name)` with an unknown incarnation (refuse to
  create a new one) until an operator adopts or discards it. Adopting
  needs the key, so a restore must also restore custody to the same
  point or later.
- **B.23b Operators must not put lifecycle rules on the journal prefix.**
  A common bucket setting ("expire objects after N days", "delete
  noncurrent versions") deletes live log objects and snapshots: silent
  loss (B.13a) or the B.12 hazard. hostd's start-up probe should read the
  bucket's lifecycle configuration where the API allows and warn.

**workerd file layout.**

- **B.24** None used. The journal and snapshots are made by the Store
  object through public APIs and by hostd.

**Migration from today's journal.**

- **B.25** Per store, driven by the lease holder's hostd once the fleet
  supports it (an ADR-041 protocol gate: a mixed fleet must not place the
  workload on an old hostd):
  1. read the mgmt journal (as today), write it as log objects or a
     snapshot at `head` (sealed rows can even be copied verbatim, X.4);
  2. write a barrier;
  3. call a new mgmt route `POST …/stores/{kind}/{name}/migrate {head}`,
     conditional on mgmt's head still being `head`; mgmt sets
     `backend=object` and from then answers host appends with `409
     store_migrated` (a new error code). A write that landed in between
     fails the condition; hostd copies the tail and retries.
  4. mgmt keeps the old rows for a retention period (rollback: copy the
     object log back and flip again), then deletes them. The key is the
     same, so crypto-shred is unchanged.
- **B.25a Correction to step 1 and X.4.** KV and R2-index journals are
  sparse (T.19), so copying their sealed rows verbatim as log objects
  breaks B.2a. Migration writes a **snapshot at `head`** (the lease
  holder's local copy, which is the journal folded, taken after a
  catch-up to mgmt's `head` and refused while the store is degraded), and
  the log starts empty at `head+1`. Only a D1 journal (gap-free) could be copied row by
  row, and it should be snapshotted anyway to drop the replay drift.
  Verbatim copy (X.4) remains useful only for C (C.15), which keeps
  today's journal semantics.
- **B.25b Reads must be refused too.** After the flip, mgmt must answer
  journal *reads* with `store_migrated` as well, not only appends.
  Otherwise a lagging host still on the mgmt path catches up from a
  frozen journal, finds nothing new and serves a stale copy as fresh.
- **B.26** Admin paths that fold the journal inside mgmt (`tw kv key
  get/put/list`, `tw r2 object list`, T.6) move to the lease holder, the
  way `tw d1 execute` already works (`/v1/store-admin`). Consequence: those
  commands need a running deployment, which `tw kv` doesn't today (Q.4).

**Failure modes.**

- **B.27** Object store without real create-if-absent or with stale LIST:
  split brain. Mitigated by the start-up probe (X.10), not eliminated; a
  backend that regresses on upgrade could pass the probe and still lose a
  race. Conformance tests per supported backend (V.2). See X.10a: Garage
  and multi-site deployments are expected to fail, and the probe has to
  race (X.10b).
- **B.28** Lost PUT response: hostd doesn't know whether `log/<k>` is its
  own. It GETs `log/<k>` and compares an append ID it put in the object
  (D1 entries already carry an `id`); same ID means success.
- **B.29** Slow object store: writes are slow; reads are fine unless a
  catch-up is due. The read-path circuit breaker of §6 is still needed,
  against the object store this time.
- **B.29a** hostd restart during an mgmt outage: the store key is only in
  hostd's memory (X.3), so after a restart the host can neither seal nor
  open until mgmt answers a key release. The store then serves reads
  from its local copy (fail static) and refuses writes. Persisting the key
  on the host (wrapped by the host's node key, or in the microVM volume)
  would close this, at the cost of a key at rest on the host (Q.3).
- **B.30** Clock assumption only in B.12 (`T_stale < T_gc`, hours apart).
  **Corrected by B.12a:** that assumption fails across a host suspend;
  B.12b removes it.
- **B.31** A host with object-store credentials can delete a tenant's log
  (B.13). Today a host can't delete mgmt rows. Object-store versioning or
  object lock limits the damage; that is operator configuration.

**Verdict.**

- **B.32** Meets G.1–G.5 for KV, D1 and R2 indexes with the same RPO 0,
  smaller exposure in mgmt, bounded growth and replay drift, and no
  private files. Costs: an object store becomes mandatory for multi-host,
  write latency becomes the object store's, a new class of backend
  correctness risk (B.27), and the largest implementation of the RPO-0
  options except D.
- **B.32a Amended verdict.** B stands as the direction, but as first
  written it had a silent-loss hole (B.12a), no way to notice a lost tail
  (B.13a), unauthenticated substrate entries (X.9a) and a narrower set of
  backends than "any S3-compatible store" (X.10a). With B.12b, B.13b, X.9a
  and B.23a it is sound on a strongly consistent backend with atomic
  create-if-absent. Its RPO 0 means "as durable as the backend never
  losing a committed object", which is the same trust today's design puts
  in mgmt's database, minus the head check.

**Alternative log backend.**

- **B.33 The log needs only three operations, and a SQL database has
  them.** B's backend contract is: create `(store, seq)` if absent,
  read a range in order, and strongly consistent reads of both. A table
  `(incarnation, seq) PRIMARY KEY` with `INSERT … ON CONFLICT DO NOTHING`
  is exactly that, so hostd can write the log straight to a Postgres
  that is *not* mgmt's (or a separate database on the same server), with
  host sealing, barriers and snapshots unchanged. No journald service (C),
  no conditional-PUT question (X.10a), latency like today's append. Costs:
  database credentials on every host (the same exposure as bucket
  credentials, B.13; per-host roles with row-level security can't follow
  leases without mgmt managing grants per lease change), large values in
  SQL (snapshots can still go to an object store or `fs`), and the
  operator runs a Postgres. It is the fallback for operators whose object
  store fails V.2 (Garage) and a better fit than C for REC.4 (Q.12).
- **B.34 Snapshots and the log can use different backends.** Nothing in
  B ties them: log in Postgres (B.33) or `fs`, snapshots in an object
  store, is a valid combination. The reverse (mgmt's database for
  snapshots only) still puts tenant data in mgmt's database and fails
  G.2.

### Option C: a separate journal service with lease-fenced appends

**Summary.**

- **C.1** Move ADR-044's journal, unchanged in shape, out of mgmt into its
  own service, `tw-journald`, with its own database (SQLite for one host,
  Postgres for HA), separate from mgmt's. mgmt only issues signed epoch
  tokens and releases keys. This is issue #38's alternative 4.

**Write path and latency.**

- **C.2** Steps: (1) D1 dry run; (2) hostd → journald `append` with a
  token `{tenant, workload, writer, epoch, host}` signed by mgmt (Ed25519)
  at lease grant, carried by heartbeat; (3) journald checks the signature
  and the epoch rule (X.8), runs today's conditional append in its
  database (including `ckey` compaction); (4) apply and answer.
- **C.3** Latency: same shape as today (one round trip plus one SQL
  transaction), on a different server. No better unless journald is
  closer to the hosts than mgmt.

**Durability, RPO and mgmt-down availability.**

- **C.4** RPO 0. Durability is journald's database (its backups, its
  replication).
- **C.5** mgmt down: writes continue as long as tokens stay valid. If
  tokens don't expire, the epoch rule alone fences (X.8) and writes survive
  any mgmt outage; then revocation (tenant delete, kill switch) needs mgmt
  to push a deny to journald. If tokens expire (TTL < lease TTL), C
  degrades to X.7. Same caveat as B.9 for workloads with DO classes.
- **C.6** journald down: writes fail, reads fail static. A new single
  point of failure, as mgmt is today.

**Fencing.**

- **C.7** Epoch token checked by the store (X.8): journald keeps the
  highest epoch per (store, writer) in the append transaction and refuses
  lower ones. This is atomic, unlike today's check-then-append (T.5). The
  barrier rule isn't needed because the service can check epochs itself.

**Custody and encryption.**

- **C.8** Either the host seals (X.3; journald sees ciphertext only) or
  journald gets custody access and seals as mgmt does (journald then
  becomes a second party that can read every tenant's data). Host sealing
  is the one that meets G.2.

**Move, takeover, host loss.**

- **C.9** As today: replay from the service. With C alone there are no
  snapshots, so full replay; C can add snapshot compaction as a service
  feature ("replace up to seq N"), issue #38's alternative 1.

**Compaction and growth.**

- **C.10** `ckey` compaction as today; D1 still unbounded until a
  snapshot entry exists. Growth moves to journald's database. Large values
  in a SQL database are the same poor fit ADR-044 noted for R2.

**Replay exactness.**

- **C.11** As today (T.9) until snapshots exist; then as B.20.

**Operator burden and new components.**

- **C.12** A new stateful service with its own database to run, upgrade,
  monitor, back up and make HA: for most operators, a second Postgres. On
  a homelab it is the same machine and, plausibly, the same binary
  (`tw-mgmt -role journal`), which removes mgmt from the path in name more
  than in fact.
- **C.13** Smallest code change of the RPO-0 options: mgmt's store module
  moves, hostd's client points elsewhere, tokens are new.

**workerd file layout.**

- **C.14** None used.

**Migration.**

- **C.15** Copy rows (sealed, X.4) from mgmt's database to journald's per
  store, flip a `backend` column as in B.25. If journald shares mgmt's
  code, the copy can be a database-level export.

**Failure modes.**

- **C.16** journald unavailable or slow: the T.10 stall problem, moved.
- **C.17** Token leak: a stolen token lets its holder write as that writer
  until a higher epoch exists. Tokens should bind the host (journald checks
  the caller's tailnet identity, WhoIs, as mgmt does for hosts).

**Verdict.**

- **C.18** Removes mgmt from the path at the cost of a second control-
  plane-like service. Reasonable if Ross wants RPO 0 without requiring an
  object store; otherwise B gets the same result with storage operators
  already run.

### Option D: peer-host replication (the lease holder streams to a follower)

**Summary.**

- **D.1** The lease holder's hostd sends each append to one or two
  follower hosts chosen by placement; a follower persists the sealed
  entries in a local journal file and acks; the holder applies and answers
  after the acks. On host loss, a follower takes the lease.

**Write path and latency.**

- **D.2** Steps: (1) D1 dry run; (2) hostd seals and sends to followers
  over the tailnet; (3) each follower checks the epoch (X.8), appends,
  fsyncs, acks; (4) apply and answer. Latency: one host-to-host round trip
  plus a follower fsync. On a LAN, lower than B or today.

**Durability, RPO and mgmt-down availability.**

- **D.3** RPO 0 for one host loss with one follower; two simultaneous host
  losses lose data. No off-site copy: a disaster that takes every host
  needs a separate backup (B's snapshots, in practice).
- **D.4** mgmt down: writes continue while the follower set is intact.
  Replacing a dead follower needs mgmt (configuration master), so a
  follower loss during an mgmt outage forces a choice between stopping
  writes and continuing unreplicated.

**Fencing.**

- **D.5** Followers check epochs (X.8). Membership (who the followers are,
  at which epoch) is decided by mgmt and versioned with the lease, as a
  primary-backup protocol with an external configuration master
  (Kafka-ISR-like). Getting reconfiguration right under failures is the
  hard part.

**Custody and encryption.**

- **D.6** Holder seals; followers store ciphertext and never get the key
  until they become the holder (then via X.3).

**Move, takeover, host loss.**

- **D.7** Takeover by a follower is fast (data is already local and it can
  replay before it serves). A move to a non-follower needs a full copy
  first. Followers must be re-seeded after every move or loss (a snapshot
  transfer).

**Compaction and growth.**

- **D.8** Each follower keeps a log and needs its own snapshotting; the
  same machinery as B, on N hosts' disks.

**Replay exactness.**

- **D.9** As B (snapshots plus tail replay); followers could replay
  continuously to stay warm, but then they run tenant SQL on another host.

**Operator burden and new components.**

- **D.10** No new service, but ≥ 2 hosts (3 for meaningful availability),
  host-to-host tailnet grants that don't exist today, follower capacity
  in placement, and the most new protocol code of all the options. A
  single-host `tw up` gets nothing.
- **D.11** It is the "custom replicated log" the RFC rejected (§11).

**workerd file layout.** **D.12** None used.

**Migration.** **D.13** As B.25, with followers seeded from the mgmt
journal.

**Failure modes.** **D.14** Split brain if membership changes and epochs
race; follower lag; follower disk full stopping the holder; network
partitions between hosts but not to mgmt.

**Verdict.** **D.15** Lowest write latency for RPO 0 and fastest takeover,
at the highest complexity and with no backup story of its own. Not
recommended for now; it could layer on B later for latency.

### Option E: DO-storage WAL or page shipping (the RFC's backup, for all DOs)

**Summary.**

- **E.1** Build the RFC's async encrypted backup (§4.9, §10): ship each
  DO's SQLite changes (WAL frames or changed pages) and periodic snapshots
  to an object store. Platform stores ride on it and drop their journal.
  Covers tenant DOs and Workflows too (T.15), which no other option does.

**Write path and latency.**

- **E.2** Writes are local; a shipper uploads batches asynchronously
  (RFC: "≤ 1 PUT/s per tenant-host while busy"). D1 executes once.
- **E.3** Synchronous shipping (ack after upload) would need a hook in
  workerd's output gate, which workerd doesn't expose. Without it, RPO is
  the shipping lag (RFC target ≤ 5 s).

**Durability, RPO and mgmt-down availability.**

- **E.4** RPO = shipping lag on host loss (non-zero); 0 on a planned move
  if the move flushes first. Reads and writes need neither mgmt nor the
  object store. That is weaker than today's RPO 0 for platform stores
  (G.3): moving stores onto E alone is a regression for KV and D1.

**Fencing.**

- **E.5** As A.6/A.7 (barrier object, epoch-named generations, a time
  bound for the old holder), and it must also fence *tenant* DO writes,
  which is what ADR-028's 30 s fence does today.

**Custody and encryption.**

- **E.6** The shipper seals with a per-workload key released at the lease,
  like the microVM volume key (ADR-024). On the microVM tier the files are
  inside the guest's dm-crypt volume, so the shipper runs in the guest
  (`tw-guestd`), or the host ships the encrypted volume at block level,
  which needs a consistent point (filesystem freeze) and ships whole
  volumes.

**Move, takeover, host loss.**

- **E.7** Restore the newest snapshot plus shipped changes into the new
  host's DO storage directory before workerd starts the workload. Restore
  must reproduce workerd's file layout exactly.

**Compaction and growth.**

- **E.8** Periodic full snapshots, delete older segments; the shipper
  needs control over checkpoints so frames aren't lost between a
  checkpoint and an upload.

**Replay exactness.**

- **E.9** Exact: physical changes, no statement replay. Fixes T.9 for
  free.

**Operator burden and new components.**

- **E.10** An object store; a shipper (`stord`, RFC §4.2) per host or per
  guest; restore drills (RFC §10).

**workerd file layout.**

- **E.11** This is the option's problem. It depends on workerd's private
  `localDisk` layout (as observed: a directory per namespace and one
  SQLite file per object, with WAL/SHM side files when in WAL mode; not
  verified against the pinned workerd, V.5), on the journal mode workerd
  picks, and on workerd's SQLite VFS honouring the locks an outside
  reader needs (Litestream-style shipping holds a read transaction to block
  checkpoints). None of that is a stable interface; ADR-012 records that
  the layout is expected to change (OSS-3) and RFC §14 P3 forbids reading
  it. The sound path is an upstream hook (a public backup or commit
  stream for DO SQLite, in the spirit of WD-1 to WD-4); Cloudflare's own
  DO storage ships WAL batches to followers and object storage
  internally, so the shape exists, but not as a self-hosting API.
- **E.11a Litestream and LiteFS are E, not new options.** Litestream
  ships WAL frames by holding a read transaction on the database file to
  control checkpoints, so it needs exactly the file access and locking
  E.11 describes. LiteFS intercepts SQLite's files through a FUSE mount,
  so it would sit under workerd's private layout (G.4) and needs FUSE on
  every host, including macOS process-tier hosts. Both give RPO = ship
  lag, not 0. They are useful prior art for E's shipper, not a way around
  its problem.

**Migration.**

- **E.12** Stores: take a first backup, then stop journaling; RPO changes
  from 0 to the lag. Tenant DOs: nothing to migrate (they have no copy
  today).

**Failure modes.**

- **E.13** A workerd upgrade that changes the layout or journal mode breaks
  backup silently or corrupts a restore; checkpoint races lose frames;
  restoring a torn file. All are detectable only by restore drills.

**Verdict.**

- **E.14** The right long-term answer for tenant DOs and Workflows, and it
  should be pursued as its own track with an upstream hook. It is the wrong
  answer for taking mgmt off the platform-store write path: it gives up
  RPO 0 and rests on private files. Platform stores should not wait for
  it.

### Option F: row-level change capture instead of statement replay

**Summary.**

- **F.1** Change what a D1 entry contains (row images instead of
  statements), not where it is stored. Combines with B or C. Listed here
  because issue #38 asks.

**Analysis.**

- **F.2** DO SQLite exposes no session extension, update hook or
  preupdate hook, and no way to register SQL functions from JS (so
  `random()` and `datetime('now')` can't be pinned either). Triggers on
  tenant tables were rejected (ADR-038: they change `sqlite_master`,
  `changes()` and trigger order, and DDL would need interception). So F
  needs an upstream workerd change first.
- **F.3** Even with a hook, row capture can make an entry huge: one
  `UPDATE t SET x = x + 1` over a million rows is a few bytes as a statement
  and a million row images as CDC, which breaks the 2 MiB entry limit
  and the PUT/append budget. A hybrid (statements, plus row images only
  when a statement is non-deterministic) needs the SQL parser D1 avoids.
- **F.4** What F would buy, exact replay of the tail, B's snapshots
  already give for everything before the tail (B.20).
- **F.4a Correction (round 3): F buys more than F.4 says.** Snapshots
  bound drift on a *rebuild*, but replicas apply every entry by replay as
  it arrives (T.9a). Between snapshots, each replica diverges from the
  writer on every non-deterministic statement. Restoring the writer's
  snapshot would re-converge them, but a replica normally catches up from
  the log, not the snapshot. Only row images (F) or a writer-side rewrite
  that pins values make replicas identical. A cheaper interim measure is
  to refuse D1 bindings in replicated workloads (or warn on them) until F
  exists (Q.15).

**Verdict.**

- **F.5** Not now. Entries are versioned (`D1_ENTRY_V`), so a row-image
  entry format can be added later if workerd exposes a hook. Ask upstream
  for it alongside E's hook.

### Option G: hybrid, per-binding durability classes (claims HY.n)

**Summary.**

- **HY.1** Let a binding choose how durable it is, in `tailworks.jsonc`
  (not `wrangler.jsonc`, so wrangler configs stay portable), for example:
  - `journal` (default): Option B, RPO 0, one object-store PUT per write;
  - `snapshot`: Option A, local writes, RPO ≤ the interval, an explicit
    opt-in for data the app can lose;
  - nothing else in v1.
- **HY.2** Same Store object, same kinds; the class changes what hostd does
  on `append` (log it, or accept it locally) and whether D1 dry-runs.
- **HY.3** Costs: two durability paths to test across move, takeover,
  host loss and upgrade (the e2e-placement and e2e-upgrade suites roughly
  double for stores); a class change on an existing store needs a
  migration step (B.25 one way, a final log-to-snapshot the other); docs
  must explain RPO to tenants, and a wrong choice loses data quietly.
- **HY.4** Verdict: worth it only if a real app is write-bound on the
  `journal` class after B lands and is measured (V.1). Don't build it
  speculatively (Q.5).
- **HY.5 A better opt-in than `snapshot`: `async`.** The same log as B,
  but hostd acknowledges after the local apply and uploads the log object
  in the background (group-committed). RPO is the upload lag (seconds),
  not A's snapshot interval, and there are no per-interval snapshot
  stalls (A.18). Fencing still needs B's barrier plus a time bound on the
  old holder's *unshipped* writes, which a takeover discards. If a
  durability class is ever built, this is the one to build, not A.

### Option H: a consensus log embedded in the hosts (added in critique round 1)

Short form: the subsections that match D are not repeated.

- **H.1 Summary.** Instead of D's primary-backup with mgmt as
  configuration master, three (or five) hosts run a Raft group, using an
  existing library (etcd's `raft`, hashicorp/raft) rather than the custom
  log the RFC rejected (D.11). Store appends are proposals; an append is
  acknowledged when a majority has it. One group per fleet (a replicated
  journal for every store) or one per store (multi-Raft).
- **H.2 What it fixes in D.** Losing a follower doesn't need mgmt
  (D.4): a majority keeps committing, and membership changes are Raft's
  own. Fencing is Raft's term, not mgmt's epoch, inside the group; the
  store's lease epoch still decides which workload host may propose.
- **H.3 Write path.** The lease holder's hostd forwards to the group
  leader (which may be another host), the leader replicates to a majority
  with fsync, then answers: two host-to-host round trips in the worst
  case. On a LAN, comparable to today; no object store needed.
- **H.4 What it costs.** A fleet-wide group is a replicated database in
  the hosts: another control-plane-shaped component, with quorum loss
  stopping every store's writes, snapshots and log compaction to run,
  disk on every member, and upgrades that must keep the group's wire
  format compatible (ADR-041). A per-store group multiplies all of that.
  It needs ≥ 3 hosts, so single-host `tw up` and two-host homelabs get
  nothing (G.5), and it needs host-to-host grants (D.10). It doesn't remove
  the need for an off-site backup (snapshots to an object store, B.34).
- **H.5 Custody.** As D.6: the holder seals, the group stores
  ciphertext.
- **H.6 Verdict.** The strongest option for RPO 0 without an external
  store, and the right comparison point if Q.1 is "no object store". It
  is more machinery than B or B.33 for the install sizes tailworks
  targets. Not recommended now; worth revisiting only for operators that
  run ≥ 3 hosts and refuse both an object store and a Postgres.

---

## 5. What this doc does not move off mgmt

- **R.1 Queues (T.13).** Every queue operation is a write (send, claim,
  ack, retry), producers and consumers are different workloads on
  different hosts, and claims need visibility timeouts. An object-store log
  fits sends but not claims and acks at message rate. A direction that
  would fit Ross's principle: the queue lives in the *consumer* workload's
  Store object (its host is the broker, durable through B), and producers
  deliver to it over the tailnet through their hostd. That is ADR-039's
  rejected option 3 plus B, and it needs its own design (Q.2).
- **R.2 Admin writes and reads** move to the lease holder under B (B.26).
  mgmt stays on the *operator* path (authentication, audit, forwarding),
  which is control-plane traffic, but no longer stores or decrypts the
  data.
- **R.3 Store metadata** (existence, incarnation, degraded record, size
  for quotas) stays in mgmt. hostd reports sizes after snapshots, so `tw
  d1 list` still shows them.

## 6. Prerequisites, whichever option wins

- **P.1 Read-path circuit breaker** (issue #38 comment, T.10). After one
  failed or slow sync, serve the local copy and retry in the background;
  coalesce concurrent syncs; give syncs a deadline far below 30 s. Needed
  today against mgmt, and under B or C against their backend. Small,
  independent, can land first.
- **P.2 Streaming, chunked export and import** (T.14). Needed for
  snapshots (X.14) and useful on its own for recovery at the 1 GiB wall.
- **P.3 Correct the docs** that say reads keep working through an mgmt
  outage without qualification (RFC §9 SLO table "running objects
  unaffected", the KV and D1 pages), per T.10 and T.11.

## 7. Comparison

Latency columns are expectations, not measurements (V.1).

| | Today (mgmt journal) | A host-local + snapshots | B object-store journal | C journal service | D peer replication | E DO-storage shipping |
|---|---|---|---|---|---|---|
| Per-write dependency | mgmt + its DB | none | object store | journald + its DB | follower host(s) | none |
| Write latency | tailnet RTT + SQL txn | local commit | one conditional PUT | RTT + SQL txn | host RTT + fsync | local commit |
| D1 executes twice | yes | no | yes | yes | yes | no |
| RPO, host loss | 0 | ≤ interval | 0 | 0 | 0 (one loss) | ≤ shipping lag |
| Writes during long mgmt outage (no DO classes) | no | yes | yes | yes (non-expiring tokens) | yes, until a follower fails | yes |
| Fencing | mgmt check per call + CAS | object-store lease, clock-bound | barrier in CAS log | epoch token in service | epoch at followers | object-store lease, clock-bound |
| Tenant plaintext through mgmt | yes | no | no | no (host seals) | no | no |
| Tenant data in mgmt's DB | yes (sealed) | no | no | no | no | no |
| Replay exactness | statements, whole history | exact | exact to snapshot, tail replays | as today, + snapshots | as B | exact |
| Growth bounded | KV yes, D1 no | yes | yes | with snapshots | with snapshots | yes |
| Private workerd files | no | no | no | no | no | **yes** |
| New components | none | object store (multi-host) | object store (multi-host) | service + database | host-to-host channel | object store + shipper + upstream hook |
| Single-host `tw up` | works | works (`fs`) | works (`fs`) | works (same box) | no benefit | works |
| Covers tenant DOs, Workflows | no | no | no | no | no | **yes** |
| Size of change | — | M | L | M | XL | XL + upstream |
| Correctness rests on | mgmt's DB transaction | object-store lease + clocks | atomic create-if-absent + strong reads (X.10a) | journald's DB transaction | epoch checks + reconfiguration | object-store lease + clocks + workerd layout |
| Lost committed tail noticed | yes (mgmt `head`) | no | only with B.13b | yes (journald head) | partly (followers) | no |
| Hosts can see other tenants' store metadata | no | yes (bucket) | yes, unless X.16 | no | followers only | yes (bucket) |

Added in critique round 1, not in the table: **B.33** (B with a Postgres
log instead of an object store) behaves like B in every row except
"Per-write dependency" (that Postgres), "Write latency" (like today) and
"Correctness rests on" (a SQL primary key). **H** (Raft in hosts) is
like D but survives a follower loss without mgmt, needs ≥ 3 hosts and is
XL.

## 8. Recommendation

- **REC.1 Adopt Option B for KV, D1 and R2 indexes.** It removes mgmt
  from every per-write and per-read path (G.1), takes tenant plaintext out
  of mgmt (G.2), keeps RPO 0 and linearizability (G.3), bounds growth and
  replay drift with snapshots, and touches no private files (G.4). It
  reuses the object store multi-host R2 installs already run, and `fs` on
  a homelab (G.5). mgmt is left with what a control plane should hold:
  which stores exist, keys in custody, leases, epochs and audit.
- **REC.2 Be clear about what B does not buy.** It swaps mgmt for the
  object store as the write dependency (B.10); write latency becomes the
  object store's and may be worse than today on a remote cloud store
  (B.5); workloads with tenant DO classes still stop after 30 s of mgmt
  outage (B.9); and the correctness of fencing now rests on the backend's
  conditional PUT (B.27).
- **REC.3 Order of work**, each step shippable on its own:
  1. P.1 (circuit breaker) and P.2 (streaming export/import): useful now;
  2. host-side sealing with key release (X.3) behind today's mgmt journal:
     mgmt stops seeing plaintext before anything else moves;
  3. the journal backend in hostd (conditional create, list, probe),
     barrier entries, `log/` objects; new stores only, behind a flag;
  4. snapshots and GC with tombstones (B.12, B.18), `skip` markers (B.20);
  5. migration of existing stores (B.25) and admin paths (B.26);
  6. retire host appends to mgmt's journal.
- **REC.4 If requiring an object store for multi-host is unacceptable**,
  choose C, shipped as a role of the mgmt binary with host-side sealing,
  epoch tokens and snapshot compaction. It meets G.2 and G.3 but keeps a
  stateful service and a second database on the path.
- **REC.1a Amended (critique round 1): B, with its corrections as part
  of the design, not follow-ups.** Sealed barriers and markers (X.9a), the
  clock-free stale-append check (B.12b), the head high-water mark and
  hash chain (B.13b), restore reconciliation (B.23a) and a racing probe
  (X.10b). The supported backends are those that pass V.2, named in the
  docs (expected: AWS S3, Cloudflare R2, MinIO, `fs` single-host), and
  "any S3-compatible store" is not the claim: Garage is expected to fail.
- **REC.1b Wording (round 3).** REC.1's "linearizability" means
  linearizable *writes* (the log order). Reads are served from the
  holder's copy, are stale by up to the kind's freshness while it is
  connected, and are unbounded under a partition (X.11a). D1 replicas
  don't hold identical copies (T.9a). B neither improves nor worsens
  either point.
- **REC.3a Order of work, amended.** Step 3 includes B.12b, X.9a and the
  hash chain from the first object written; B.13b's mark and B.23a's
  reconciliation land before step 5 (migration), because migration is when
  a lost tail would first matter for existing data. Migration is
  snapshot-based (B.25a) and refuses reads as well as appends (B.25b).
  The prefix-delete task (B.15a) ships with step 3, since new stores
  already leave objects behind when deleted.
- **REC.4a The fallback, amended.** If an object store is unacceptable
  (Q.1) or the operator's fails V.2, prefer B with a Postgres log (B.33)
  over C: same protocol, no new service, no mgmt role. C is better only
  if per-host access control to the journal (C.17's WhoIs check) matters
  more than running one service fewer (Q.12).
- **REC.5 Don't build A or G now.** Revisit a `snapshot` class only with a
  measured write-bound app (HY.4).
- **REC.6 Pursue E separately**, for tenant DOs and Workflows, starting
  with an upstream ask for a public DO-storage backup/commit hook, and ask
  for F's change hook in the same conversation. Neither blocks REC.1.
- **REC.7 Reject D** for now (D.15).
- **REC.8 Queues stay in mgmt** until R.1 has its own design.

## 9. Open questions for Ross

- **Q.1** Is an S3-compatible object store an acceptable hard requirement
  for multi-host installs (B.21), given R2 already needs one? If not, REC.4.
- **Q.2** Queues: accept that they stay in mgmt for now, or start the
  consumer-hosted broker design (R.1) in parallel?
- **Q.3** Is it acceptable that the lease holder holds the store data key
  (X.5)? Today it holds the plaintext but not the key.
- **Q.4** `tw kv key put/get/list` would need a running deployment (B.26).
  Acceptable, or should mgmt keep a direct admin path to the object store
  (mgmt then needs the backend credentials and the key, and is back on an
  operator data path)?
- **Q.5** Do you want durability classes (Option G, HY.1) at all, ever? Or is "RPO 0,
  use a Durable Object for write-heavy data" the product line?
- **Q.6** Per-epoch keys (X.6): worth the custody complexity so a host
  that lost a workload can't read its later data?
- **Q.7** Should the ADR-028 30 s fence be revisited for workloads whose
  only state is platform stores plus `fence: never` classes? Under B their
  stores don't need it.
- **Q.8** Object names carry tenant and workload IDs (B.3), like R2 blobs.
  Fine, or HMAC them?
- **Q.9** With mgmt's database no longer at risk, keep the 1 GiB per-store
  limit (D1's own) or make it an operator setting?
- **Q.10** Should a mgmt backup restore still roll tenant data back (B.23)?
  Under B it doesn't; that is a change in what "restore mgmt" means.
- **Q.11** Stale reads under a partition are unbounded today and under
  every option (X.11a). Accept that as "fail static", or add a `maxStale`
  cap after which a cut-off holder refuses reads?
- **Q.12** Fallback when there is no usable object store: B with a
  Postgres log written by hosts directly (B.33, every host holds the
  database credential) or C's journald (one more service, but it can check
  each caller's identity)?
- **Q.13** Should hosts get lease-scoped backend credentials (X.16) so a
  compromised host can't list or delete other tenants' logs? It puts a
  credential-minting secret in mgmt and works only on backends with STS-
  style credentials.
- **Q.14** Garage: tell Garage users B needs a different store (or
  B.33), or wait for Garage to ship a conditional create?
- **Q.15** D1 in replicated workloads: replicas hold different values for
  non-deterministic SQL today (T.9a). Refuse or warn on the combination
  until row capture (F) exists, or document it?

## 10. To verify before accepting

- **V.1** Measure append latency today (host → mgmt with SQLite and with
  Postgres) against a conditional PUT to a LAN MinIO and to one cloud
  object store, p50 and p99, single writer and 8 concurrent.
- **V.2** Conditional PUT (`If-None-Match: *`) and read-after-write LIST on
  each backend we'd claim (MinIO, Garage, SeaweedFS, AWS S3, Cloudflare R2,
  `fs`), at the versions we'd document, as an automated conformance test.
- **V.3** The barrier rule under fault injection: two hosts, one
  partitioned, forced takeover, replicas, lost PUT responses, GC racing a
  stale writer (B.12). An e2e-placement scenario per case.
- **V.4** Snapshot time and write stall for a 1 GiB D1 database (X.14).
- **V.5** workerd's on-disk layout, journal mode and locking on the pinned
  version, if E is pursued.
- **V.6** Suspend: put a host to sleep longer than `T_gc` and the lease
  TTL during a forced takeover, wake it, and check that no acknowledged
  write is lost (B.12a/B.12b) and that the ADR-028 fence (T.18) behaves.
- **V.7** Lost tail: delete the newest log objects behind hostd's back
  and check the next holder degrades rather than serves (B.13b).
- **V.8** Restore drill: restore mgmt from a backup older than a store's
  creation and check reconciliation holds the orphan (B.23a).
