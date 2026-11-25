# ADR-036: R2 buckets as a platform store: the index in the journal, the bytes in an operator-configured blob backend that hostd talks to

**Date:** 2026-11-15
**Status:** Accepted (2026-11-16, built and shipped in v0.2.0: `r2_buckets`,
`tw r2`, [docs/r2.md](../../r2.md); results in
`status/r2-bucket-binding-issue-16.md`).

**Context.**

- Issue #16 asks for the R2 binding (`r2_buckets` in `wrangler.jsonc`;
  `env.BUCKET.get/put/head/delete/list`, `R2Object`/`R2ObjectBody`,
  `onlyIf`, `httpMetadata`/`customMetadata`, ranged reads; multipart
  upload as a stretch). Tailworks had no R2 support.
- **workerd already has the frontend.** Its `r2Bucket` binding turns the
  R2 API into HTTP requests to a service you name (with `entrypoint` and
  `props`): `GET` with the operation as JSON in a `cf-r2-request` header
  (head, get, list), or `PUT` whose body is that JSON (length in
  `cf-r2-metadata-size`) followed by the object bytes (put, delete,
  multipart). Answers are the object's metadata as JSON, then its bytes;
  errors carry `cf-r2-error`. Argument checking, `R2Object`, `.text()`,
  `writeHttpMetadata()` and so on are all workerd's. miniflare implements
  the backend side for `wrangler dev`; tailworks has to supply one.
- **ADR-044** gives a storage binding a platform-owned SQLite
  Durable Object, fed by a fenced, sealed journal in mgmt, so its data
  survives a move and two hosts never both write. It says R2 fits only for
  small objects there: mgmt's database is no place for large blobs.
- ADR-034: backend credentials never enter the workload's `env` (or its
  isolate). ADR-014: outbound dependencies are declared.

**Design questions.**

1. **Backend: where are the bytes, and where is that configured?**
2. **Who makes the S3 call?**
3. **Tenant isolation.**
4. **Streaming.**
5. **Operator surface.**

**Decision.**

1. **Split R2 into an index and a blob store.** An R2 bucket is a platform
   store of kind `r2` (ADR-044). The store's Durable Object
   holds the **index**: key → version, size, etag, upload time, HTTP and
   custom metadata, checksums, and the blob(s) holding the bytes. The
   index is journaled to mgmt like any store, so it is durable off-host,
   fenced to the lease holder, sealed at rest, linearizable, and follows
   the workload when it moves. Each index entry is small (a key ≤ 1 KiB,
   custom metadata ≤ 8 KiB).
   The **bytes** live in a blob backend. A blob is immutable and has a
   random name; a put writes a new blob, and then commits the index entry
   that points at it.
   - This is miniflare's own layout (a SQLite index plus a blob store),
     with the platform store as the SQLite.
   - It makes `onlyIf` exact. The condition is evaluated in the store
     object against the index, inside the journal's conditional append (a
     write that loses a race re-checks it), so conditional puts don't
     depend on the backend supporting `If-Match`.
   - List (prefix, delimiter, cursor, `startAfter`, `include`) is SQL on
     the index, so the backend only needs put, ranged get and delete of
     whole blobs. Any S3-compatible service, or a directory, will do.
   - Reads are strongly consistent: a read catches up with the journal
     first (a cheap call when nothing changed), as R2 is
     read-after-write consistent. If mgmt can't be reached, the store
     serves its local index after waiting at most 2 s (fail static,
     ADR-044 §8).

2. **The backend is per host, set by the operator.** `tw-hostd
   -r2-backend fs|s3` (default `fs`):
   - `fs`: files under `<data-dir>/r2` on the host. For `tw up` (homelab)
     and `tw dev`. The index still survives a move, but the bytes don't:
     on a new host a get of an old object fails with `r2_blob_missing`. A
     multi-host install uses `s3`.
   - `s3`: any S3-compatible service (MinIO, Garage, SeaweedFS, AWS S3,
     Cloudflare R2): `-r2-s3-endpoint`, `-r2-s3-bucket`, `-r2-s3-region`,
     `-r2-s3-prefix`, path-style or virtual-host addressing. Credentials
     come from the environment of tw-hostd (`TW_R2_S3_ACCESS_KEY_ID`,
     `TW_R2_S3_SECRET_ACCESS_KEY`, or the `AWS_*` names) or a file
     (`-r2-s3-credentials-file`, same `KEY=value` form). Every host that
     can run the workload must point at the same backend, as they must
     already point at the same mgmt.
   - Not per binding: a tenant naming its own S3 endpoint and credentials
     would put credentials in the bundle or the workload's env, which
     ADR-034 rules out, and would let a workload choose where hostd
     connects. Not per tenant in v1: tenants on one host share the
     operator's backend, separated by prefix (decision 4). A per-tenant
     backend, with its credentials in mgmt custody and released to the
     lease holder like secrets, is a natural extension (Open).

3. **hostd makes the S3 call; the workload never holds a credential.**
   The tenant's `env.BUCKET` is workerd's native `r2Bucket` binding,
   pointed at the platform worker's `R2Backend` entrypoint with props
   `{kind: "r2", name}` (tenant code can't change them). `R2Backend`
   (trusted platform code) speaks the R2 protocol: it asks the bucket's
   store object for the index row and evaluates `onlyIf` and the range,
   and streams bytes to and from hostd's local host API
   (`/v1/r2/blob`, on the platform's `HOSTD` binding, which tenant code
   can't reach). hostd holds the backend and its credentials.
   - The S3 traffic is platform traffic from the host (like hostd's calls
     to mgmt), not workload egress: the workload's declared dependency
     (ADR-014) is the bucket binding in its `wrangler.jsonc`, and tailnet
     policy keeps governing everything the workload itself can reach.
   - On the microVM tier the platform worker runs in the guest; the bytes
     cross the guest NIC to hostd's host API, exactly like the store
     journal. No credentials or backend address enter the guest.
   - Facet-mode objects (Dynamic Workers) can't hold a native binding, so
     a facet's `env.BUCKET` is a small shim in the facet that rebuilds
     `R2Object`/`R2ObjectBody` around an RPC stub to `R2FacetProxy`, which
     calls the platform's own native binding of the same bucket.

4. **Tenant isolation is by path, computed by hostd.** A blob's backend
   name is `<prefix>/t/<tenant-id>/w/<workload>/b/<bucket>/<blob-id>`.
   hostd builds it from the tenant and workload of the instance whose
   platform called (it doesn't take them from the request), checks that
   the deployment declares an `r2_buckets` binding for `<bucket>`, and
   accepts only blob IDs it generated (32 hex characters). So no request a
   workload can cause names another workload's or tenant's data, and an
   object key (any UTF-8, up to 1 KiB) never becomes a path at all. A
   workload's replicas (`<w>--r<n>`) use `<w>`'s blobs, as they use its
   stores. hostd knows a replica by that name alone, while mgmt scopes
   its stores from the deployment record, so mgmt gives such a name to
   nothing else: a workload that is not a replica can't be deployed as
   `<w>--r<n>` (`replica_conflict`; #348, `internal/replicaname`). Tenant
   and workload names are validated (no `/`, no `..`) before they become
   path elements.

5. **Bodies stream end to end.** workerd sends a put body with its length;
   `R2Backend` passes the stream on (a `FixedLengthStream`, after taking
   the JSON prefix off), hostd streams it to the backend through MD5 (the
   etag) and any requested SHA digests, and refuses a body that is longer
   or shorter than declared. A get streams the backend's ranged response
   through hostd and `R2Backend` behind the metadata prefix. hostd never
   holds more than a copy buffer per request; the fs backend writes to a
   temporary file and renames it; the S3 backend sends one
   `PutObject` with `Content-Length` and an unsigned payload (signed
   headers, SigV4). Objects are limited to R2's 5 GiB − 5 MiB.

6. **Multipart upload** (`createMultipartUpload`,
   `resumeMultipartUpload`, `uploadPart`, `complete`, `abort`). Each part
   is a blob; the upload and its parts are index entries, and completing
   commits the object row (listing its parts) and closes the upload in one
   append. A ranged read of a multipart object reads only the parts it
   overlaps. Part rules follow R2 (all parts but the last the same size,
   ≥ 5 MiB).

7. **Garbage.** Blobs an index change no longer references (an overwrite,
   a delete, an aborted upload) are deleted by the writer right after its
   append succeeds. A blob written for a put that then failed (a
   precondition, a conflict that changed the answer, mgmt down) is deleted
   at once. A crash between the two leaves an orphan blob (bytes nothing
   points to); a sweep that lists the bucket's prefix against the index is
   future work. When the bucket's store is deleted
   (`tw r2 bucket rm`), the store object empties itself on its next
   use (it sees a new incarnation) and deletes the blobs its old index
   pointed to. It doesn't delete the bucket's whole prefix: a replica
   might already be writing blobs for the new incarnation there.

8. **Operator surface.** A bucket is a store of kind `r2`, so the stores
   API (ADR-044) already covers it: `tw r2 bucket list|create|
   delete <workload> [<bucket>]` call `GET|PUT|DELETE
   …/workloads/{w}/stores/r2/{bucket}`, and `tw r2 object list` reads the
   bucket's journal (admin) and folds it into the live keys. No new mgmt
   endpoint. (Amended 2026-11-25, #493: superseded by `GET
   …/stores/r2/{bucket}/objects` (`admin|tenant:data`, like KV's
   `…/keys`). mgmt folds the index journal itself (`internal/r2wire`,
   streaming over journal pages and holding only the requested prefix),
   so the CLI no longer pages the raw journal or carries a copy of
   store-r2.js's fold and its entry-version check. An index entry from a
   newer tailworks is `409 version_skew`, never skipped. Against an older
   mgmt, `tw r2 object ls` fails with `not_found` and says to upgrade
   mgmt, as ADR-041 §7 allows.) Object bodies (`tw r2 object get|put`) need the blob
   backend, which only hosts have; they need an mgmt endpoint proxied to
   the lease holder (as `tw logs` is) and are a follow-up.

**Consequences.**

- A put costs one blob write plus one journal append (a round trip to
  mgmt); a get costs a catch-up check plus one ranged blob read. This is
  slower than a local miniflare, and fine for R2-style object storage.
- With the `fs` backend, bytes are as durable as the host's disk (the
  same as Durable Object storage on the process tier) and are not
  encrypted by tailworks. With `s3`, durability and encryption at rest are the
  backend's.
- Writes need mgmt (as for every store); reads keep working from the
  local index while mgmt is down, as long as the backend is up and the
  workload runs: one with a fenceable Durable Object class stops after
  30 s without mgmt (ADR-028 §3, ADR-044 §8).
- Cloudflare's bucket-level features are not here: public buckets and
  custom domains, presigned URLs and the S3 API to the bucket itself,
  lifecycle rules, event notifications, storage classes (`storageClass`
  is always `Standard`), `ssecKey`, and the `jurisdiction` field.

**Open.**

- Per-tenant backends with credentials in mgmt custody.
- An orphan-blob sweep; deleting a workload's blobs when it is deleted
  (today they go when the bucket's store is deleted and next used, and
  only from a host whose store object had them indexed).
- `tw r2 object get|put` through mgmt to the lease holder.
- Sharing a bucket between workloads (needs the stores' grant model).
- Bucket mounts for containers (ADR-032): a FUSE or virtio-fs view of a
  bucket in the container VM, served by hostd from the same index and
  blobs.
