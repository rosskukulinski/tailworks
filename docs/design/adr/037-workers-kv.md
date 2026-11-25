# ADR-037: Workers KV is a platform store per workload, behind workerd's own kvNamespace binding

**Date:** 2026-11-15
**Status:** Accepted (2026-11-16, built and shipped in v0.2.0: `kv_namespaces`,
`tw kv`, [docs/kv.md](../../kv.md); results in
`status/workers-kv-binding-issue-15.md`).
Builds on [ADR-044](044-platform-stores.md).

**Context.**

- Issue #15: tailworks had no Workers KV binding. `kv_namespaces` in
  `wrangler.jsonc` was ignored, `env.KV` was `undefined`, and libraries
  that need KV didn't run. The most common one is
  `@cloudflare/workers-oauth-provider`, which keeps clients, grants and
  tokens in `env.OAUTH_KV`.
- ADR-044 gives every storage binding a platform-owned SQLite
  Durable Object per store, a write-ahead journal in mgmt that is fenced to
  the lease holder, sealed at rest and compacted by key, and the plumbing
  to bind workerd's native binding types to a platform backend. KV is its
  first kind.
- Measured on the pinned workerd (1.20261108.1): the `kvNamespace` binding
  is a client of an HTTP protocol, the one miniflare's KV gateway serves:
  - `GET|PUT|DELETE /<key>?urlencoded=true` with `cache_ttl`,
    `expiration` and `expiration_ttl`;
  - metadata in the `CF-KV-Metadata` request and response header, and
    `CF-Expiration` on a GET response;
  - `GET /?key_count_limit&prefix&cursor` for `list()`, with metadata
    returned as JSON *text*;
  - `POST /bulk/get` with `{keys, type, withMetadata}`.

  workerd itself checks empty and over-long keys, and parses `json` /
  `arrayBuffer` / `stream`. It does not check TTLs. A `put()` waits until
  the backend has read the **whole** request body: an error answered before
  that hangs the caller, and `body.cancel()` is not enough. For GET-shaped
  operations the tenant's error message is the response's `statusText`.

**Decisions.** The issue's five design questions, in order.

1. **Where the data lives and who owns it: a platform store.** Each
   namespace is a store of kind `kv`: the platform's `Store` Durable Object
   (SQLite) serves reads and writes on the workload's host, and every write
   is in mgmt's journal before it is acknowledged. So a namespace outlives
   hostd restarts, lost DO storage and moves to another host (forced
   takeover, ADR-028). It keeps the substrate's properties: the lease
   holder alone may write (fencing), entries are sealed at rest, and
   deleting a namespace crypto-shreds it. On the microVM tier the object's
   SQLite is on the guest's encrypted volume (ADR-024).

   Two options were rejected. A **mgmt-store-backed** KV (every operation
   a call to mgmt) has no local reads, and an mgmt outage takes reads down
   too. A **DO alone**, with no journal, loses the data on a move. See
   ADR-044, options 1 and 2.

   - **Binding.** A tenant's `env.<BINDING>` is workerd's own
     `kvNamespace` binding. Its target is the platform's `KVBackend`
     entrypoint, with props `{kind: "kv", name}` that tenant code can't
     change. So the API surface and its argument checks are upstream's
     (`get` with every type, `getWithMetadata`, bulk `get`, `put` with
     `expiration`/`expirationTtl`/`metadata`, `delete`, `list`). A facet
     (Dynamic Worker) can't hold a native KV binding. It gets an RPC stub
     to `KVFacet`, which forwards to a native binding of the same namespace
     held by the platform worker, so the API and errors are the same.
   - **Entries.** One journal entry per write:
     `0x01 | uint32 header length | {k, x?, m?, d?} | value`, where the
     compaction key is the KV key, so the journal stays the size of the
     live data. Go reads and writes the same format (`internal/kvwire`,
     pinned by a test). mgmt's operator view (below) is the only place in
     mgmt that decodes it; the substrate stays opaque.
   - **Expiry.** An expired key is invisible to reads at once. The object's
     alarm then journals a delete for each expired key (in batches of 128,
     conditional like any write), so every host, and a catch-up from
     scratch, drops it too. A replica whose alarm loses the race finds
     nothing left to delete.

2. **Scope and sharing: per workload.** A namespace belongs to one workload
   (and its replicas), as every platform store does. Its name is the
   `kv_namespaces` entry's `id`, or the binding name if there is no `id`.
   Bindings of one app with the same `id` share the namespace. Two
   *workloads* can't bind the same namespace in v1: each workload that
   names `oauth` gets its own.

   Sharing needs a grant model, and the design leaves room for one. The
   journal is already the serialization point, so a second workload's store
   object could append to and catch up from the same journal. What's
   missing is authorization. The owner workload would declare who may bind
   a namespace (a tailnet grant from the binder's tag to the owner's, with
   a `kv` capability naming the namespace and `read`/`write`). mgmt would
   check it when the binder deploys and on every journal read and append,
   with the binder's own lease as the fence. That is tag-based identity,
   not a token, as everywhere else in tailworks. It is deferred until someone
   needs it. Cloudflare shares by account; on tailworks, call the owning workload
   over the tailnet instead (`getCaller` tells it who is asking).

3. **Consistency: read-your-writes per host, bounded staleness across
   writers.**
   - A namespace's reads and writes on one host go through one object, so
     a workload always reads its own writes. Cloudflare KV only promises
     that in the same location, and typically within 60 s elsewhere.
   - A write by another writer (another replica, `tw kv key put`) is seen
     on the next read that wants data fresher than the object's last sync:
     60 s by default (Cloudflare's `cacheTtl` default), or the read's
     `cacheTtl` (at least 30 s, as on Cloudflare). The object's next write
     catches up at once (the conditional append).
   - Writes are linearizable (the journal). Concurrent writes to one key
     from two replicas are ordered by mgmt. On Cloudflare the last write
     wins by timestamp.
   - If mgmt is unreachable, writes fail (`503`, `KV PUT failed: 503 …`).
     Reads answer from the local copy if this object has synced on this
     host before, after waiting at most 2 s for mgmt (then without
     waiting until it answers again), and fail on a host that never has
     (ADR-044 §8). A workload with a fenceable Durable Object class stops
     altogether after 30 s without mgmt (ADR-028 §3). A write costs one
     round trip to mgmt; reads are local SQLite.

4. **Limits.** Cloudflare's published limits, except value size:
   - keys are 1 to 512 bytes of UTF-8, not `.` or `..`;
   - metadata is at most 1024 bytes of JSON;
   - `expirationTtl` is at least 60 s, and an absolute `expiration` must be
     at least 60 s in the future;
   - `list()` returns at most 1000 keys a page (`limit`, default 1000), in
     UTF-8 byte order, with an opaque `cursor` (base64url of the last key
     returned) and `list_complete`;
   - a bulk `get` takes 1 to 100 keys, `text` or `json`.

   **Values are at most 2 MiB − 4 KiB** (Cloudflare: 25 MiB). A value is
   one journal entry, a substrate entry is at most 2 MiB, and the header
   needs room. Larger values belong in an object store (the R2 work), not
   in mgmt's database. The substrate's per-store limit (1 GiB of live
   data) applies. Errors use Cloudflare's statuses and wording (`400`,
   `413`, `414`). Store failures are `503`.

5. **Operator surface.**
   - **CLI:** `tw kv namespace create|list|delete` and
     `tw kv key put|get|delete|list`, like `wrangler kv`. Key commands
     name the namespace with `--binding` (looked up in the app's
     `wrangler.jsonc`) or `--namespace`. (Since #458: `--namespace` takes
     the name or the binding, `--binding` is the same flag, and the app's
     only `kv_namespaces` entry is the default; the verbs are spelled
     `ls` and `rm`, with `list` and `delete` kept.) A value comes from an argument,
     `--path` or stdin, with `--ttl`, `--expiration` and `--metadata`.
   - **REST** (admin only; `api/openapi.yaml`):
     `GET …/stores/kv/{name}/keys` (prefix, cursor, limit) and
     `GET|PUT|DELETE …/stores/kv/{name}/value?key=`. Namespaces themselves
     are the substrate's `…/stores/{kind}/{name}` routes. mgmt answers
     reads by folding the namespace's journal, so the operator sees every
     acknowledged write, even one no host has applied yet. Writes are
     conditional appends with Cloudflare's rules. An admin's key write is
     audited (`kv_put`, `kv_delete`) with the namespace and who wrote, but
     not the key: key names can be sensitive, and mgmt keeps them only
     sealed and HMACed. A binding's writes are not audited (high volume),
     like every journal append. Namespace create and delete are audited.
   - **`tw dev`:** namespaces live in the dev process's in-memory mgmt.
     They are saved to `<app>/.tailworks/dev-stores.json` (owner-only, and the
     directory git-ignores itself) when `tw dev` stops, and loaded when it
     starts, so local KV lasts between runs, as `wrangler dev`'s local
     state does.

**Consequences.**

- `workers-oauth-provider` runs unmodified, with `OAUTH_KV` on tailworks.
  Running it surfaced a runtime bug, now fixed: a default export that is a
  class instance (`export default new OAuthProvider(...)`) lost its
  prototype `fetch()`. It also surfaced a difference worth documenting:
  the provider compares `request.url`'s origin with its HTTPS resource,
  and tailworks hands Workers `http://` URLs.
- KV writes need mgmt. That is the right trade for KV, which is read-heavy
  with slow writes on Cloudflare too. Write-heavy state belongs in a
  Durable Object.
- KV data counts against mgmt's database size and backups, sealed.
- Tombstones (deletes, expiries) stay in the journal until the key is
  written again. Tombstone compaction is a substrate follow-up, and matters
  most for token-like workloads with many short-lived keys.

**Open.**

- Sharing a namespace between workloads, gated by tailnet grants (above).
- Tombstone compaction (substrate).
- Values above 2 MiB (with an object store).
- Bulk REST writes (`wrangler kv bulk put/delete`) and a `tw kv` import
  from a Cloudflare export.
