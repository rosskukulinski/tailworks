# ADR-044: Platform-owned SQLite Durable Objects with a fenced journal in mgmt back storage bindings

**Date:** 2026-11-15
**Status:** Accepted (2026-11-16, built and shipped in v0.2.0: KV, D1 and R2
run on it, ADR-036 to ADR-038; results in `status/platform-stores.md`).
Drafted as `ADR-platform-stores`.

**Context.**

- Workers KV (issue #15), Queues, D1 and R2 are all bindings whose data
  belongs to the platform, not to one Durable Object of the app. Tailworks had
  none of them. Each needs the same things: a place for the data, a way for
  it to outlive the host, fencing so two hosts never both write, encryption
  at rest, and a binding the tenant can't point at someone else's data.
- **Upstream already has the frontends.** Stock workerd's `kvNamespace`,
  `queue` and `r2Bucket` bindings turn the Cloudflare APIs (`env.KV.get`,
  `put` with `expirationTtl` and `metadata`, `list`, …) into HTTP requests
  to a service you name, with `entrypoint` and `props`. They store nothing
  themselves (miniflare supplies the storage for `wrangler dev`). Measured
  on the pinned workerd: props reach the target entrypoint unchanged, so
  they can carry the store's identity. A native binding can't be passed into
  a Dynamic Worker's env (`DataCloneError: A KV namespace binding cannot be
  serialized`), so facet Durable Objects need a forwarding stub.
- **DO storage does not move.** A workload's DO storage is a directory on
  its host (`<data>/tenants/<t>/workloads/<w>/do`; dm-crypt on the microVM
  tier, ADR-024). The RFC's asynchronous encrypted backup (§failover) is not
  built. A workload that moves host (`tw deploy --replace`, a forced
  takeover after a host loss, ADR-028) starts its Durable Objects empty. A
  KV namespace has to survive that.
- **Leases and fencing exist.** mgmt releases secrets and volume keys only
  to the host holding the workload's lease at its epoch (ADR-011/028,
  ADR-034), and hosts self-fence stateful workloads after 30 s without a
  heartbeat.

**Options considered.**

1. *mgmt's database is the store, and every operation is a call to mgmt.*
   It survives moves and shares trivially. But every read is a round trip
   over the tailnet, and an mgmt outage takes reads down with it. Workers
   are supposed to keep serving through mgmt outages (ADR-028 §3).
2. *A Durable Object alone* (the store lives in the workload's DO storage).
   Reads and writes are local and the code is reused, but data is lost on
   every move. It doesn't meet the issue's acceptance criteria.
3. *A Durable Object plus shipping its SQLite file or WAL* to a bucket
   (the RFC's backup). This needs a blob store, workerd's private file
   layout (ADR-012 rules that out) and a restore step. Writes acknowledged
   between two shipments are lost when a host dies.
4. **A Durable Object that serves the data, with a write-ahead journal in
   mgmt.** (Chosen.)

**Decision.**

1. **One platform-owned SQLite Durable Object per store.** The platform
   worker (trusted; tenant code never runs there) gets one DO class,
   `Store`, with SQL enabled. Each store is the object
   `getByName("<kind>:<name>")`, in the workload's DO storage, so the
   microVM tier keeps it on its encrypted volume. A kind (KV, …) is a
   platform module that registers how to apply an entry to its own tables
   and which operations its backend may call (`hostd/gen/js/store.js`).

2. **Tenant code gets upstream's binding.** A kind names the workerd
   binding type its env entry uses (`kvNamespace`, `queue`, `r2Bucket`, or
   a plain `service`) and the platform entrypoint it targets, with props
   `{kind, name}` written into `config.capnp` by hostd. So the API surface
   and its argument handling are workerd's, and the store a binding reaches
   is fixed at deploy. Facets get an RPC stub to a per-kind proxy
   entrypoint, which forwards each call to a native binding of the same
   store held by the platform worker (`__TW_STORE_<Binding>`).

3. **Journal first, then apply.** A change is one or more opaque entries.
   `Store.commit` sends them to mgmt (`POST
   /v1/tenants/{id}/workloads/{w}/stores/{kind}/{name}/journal`) through
   hostd's local API, then applies them in one SQLite transaction. The
   append is conditional: it succeeds only if the journal's head is the
   one the object has applied (`after`, and the store's `incarnation`).
   Otherwise mgmt answers `409 store_conflict`, the object catches up and
   the write is retried.
   - The object catches up (`GET …/journal?after=`) the first time it is
     used after a start, after a conflict, and when a read wants data
     fresher than its last sync. Catch-up runs the same `apply` as a local
     commit, so local state is always a replay of a prefix of the journal.
   - An entry a kind can't apply stops the replay before it: the object
     keeps what applied, marks itself degraded (the failing seq and
     error), serves reads, refuses writes with `store_degraded`, and
     retries the entry on later syncs. It never skips an entry or reports
     itself synced past one.
   - An acknowledged write is in mgmt's database before the caller sees it:
     RPO 0 for a host loss or a move. The new host's object starts empty
     and replays the journal.
   - The journal is linearizable across writers: replicas of a workload,
     an admin writing through the API (`tw kv key put`), or two copies of
     one object during a move. A writer that is behind can't overwrite
     anything.

4. **Fenced like secrets.** hostd adds the tenant, the workload and the
   lease epoch it holds. mgmt serves reads and appends only to the host
   holding that lease at that epoch (`409 lease_lost`, audited `deny`),
   exactly as for secret grants. A host that lost the workload can neither
   read nor write. Admins read and write without an epoch; the role gate
   (`x-tw-role: admin|host`) keeps everyone else out. hostd lets the
   platform reach only the stores the running deployment declares.

5. **Compaction by key, not by snapshot.** An entry may carry a compaction
   key (`ckey`). mgmt deletes every earlier entry with the same `ckey` in
   the append's transaction. For KV the key is the KV key, so the journal
   stays the size of the live data, and a catch-up from any point is still
   complete: an entry is only removed when a later one replaces it. Kinds
   whose entries don't replace each other (a queue's messages) must write a
   removing entry, such as an ack, with the same `ckey`.

6. **Sealed at rest, crypto-shredded on delete.** Each store has a random
   `incarnation` and a 32-byte data key in custody
   (`tw/<tenant>/stores/<incarnation>/key`). mgmt seals each entry with
   AES-256-GCM, bound to the incarnation and seq. It keeps an HMAC of the
   `ckey`, never the key itself. Deleting a store deletes its rows and its
   key, so a database backup taken earlier can't be read. A store object
   that sees a different incarnation (the store was deleted, or deleted
   and created again) empties itself.

7. **Scope: the workload.** A store belongs to one workload (`(tenant,
   workload, kind, name)`). A workload's replicas (`<w>--r<n>`) use the
   workload's stores, each fenced by its own lease (the conditional append
   keeps them consistent). Sharing a store between workloads is not in v1.
   The design allows it, since the journal is the serialization point, but
   it needs a grant model; the KV ADR covers that.

8. **Failure modes.** If mgmt (or the path to it) is down, writes fail with
   `store_unavailable`. Reads keep answering from the local copy if this
   object has caught up on this host before (fail static, like Workers
   during an mgmt outage, ADR-028), and fail if it never has (a new host
   has nothing true to serve).

9. **Limits.** At most 2 MiB per entry, 4 MiB and 128 entries per append,
   1 KiB per `ckey`, and 1 GiB of kept data per store (`400
   store_limit`). Reads return pages of up to 1000 entries or about 4 MiB.
   The host role's 1 MiB request limit is raised to 6 MiB for appends
   only.

**Consequences.**

- KV, Queues and D1 can be built as a kind (a module plus a backend
  entrypoint) without touching mgmt, hostd's fencing or the generator. Each
  kind decides its entry encoding and its compaction keys. R2 fits only for
  small objects: large blobs belong in an object store, not in mgmt's
  database. That is out of scope here.
- Every write costs one round trip to mgmt and a database transaction.
  Reads are local SQLite. This is the right trade for KV, which on
  Cloudflare is read-heavy and has slow writes. It is a poor fit for
  high-rate write paths (use a Durable Object).
- mgmt's database now holds tenant data (sealed). Its size and backups
  grow with the stores.
- The store object's copy is a cache of the journal plus local indexes, so
  losing a host's DO storage loses nothing acknowledged.
- `tw dev` uses the in-memory fake mgmt, so dev stores last as long as the
  `tw dev` process. They aren't persisted between runs.

**Open.**

- The microVM tier should work unchanged (the Store object runs in the
  guest's workerd and reaches hostd's API over the guest NIC), but it
  hasn't been run there yet.
- Compaction of tombstones (deletes of keys that are never written again)
  and a per-tenant quota across stores.
- Sharing a store between workloads, gated by tailnet grants.
