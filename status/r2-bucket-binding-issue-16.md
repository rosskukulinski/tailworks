Last update: 2026-11-15 09:40 PT. R2 bucket binding (issue #16, ADR-036): built and tested on the process and microVM (vz) tiers, fs and S3 backends.

# R2 buckets (`r2_buckets`)

Design: [ADR-036](../docs/design/adr/036-r2-buckets.md). User docs:
[docs/r2.md](../docs/r2.md). Builds on the platform-store substrate
([ADR-platform-stores](../docs/design/adr/platform-stores.md),
`status/platform-stores.md`).

**Shape.** A bucket is a platform store of kind `r2`. The store's object
holds the **index** (key → version, size, etag, uploaded, HTTP and custom
metadata, checksums, blob IDs), journaled to mgmt, fenced, sealed, and
replayed on a new host. The **bytes** are immutable blobs in the host's R2
backend (`-r2-backend fs|s3`), which only hostd talks to. The tenant gets
workerd's native `r2Bucket` binding pointed at the platform's `R2Backend`
entrypoint, which speaks workerd's R2 protocol (the one miniflare serves)
and streams bytes to and from hostd's `/v1/r2/blob`.

## Built

- **hostd/r2** (new package): `Backend` (put/ranged get/delete of immutable
  blobs), `FS` (temp file + fsync + rename), `S3` (PutObject with
  Content-Length and `UNSIGNED-PAYLOAD`, ranged GetObject, DeleteObject;
  path style or virtual host), a minimal SigV4 signer (`sigv4.go`, checked
  against AWS's published S3 example), `Flags` for twd-hostd
  (`-r2-backend`, `-r2-dir`, `-r2-s3-endpoint|bucket|region|prefix`,
  `-r2-s3-virtual-host`, `-r2-s3-credentials-file`; credentials from
  `TWD_R2_S3_*` or `AWS_*` env). `Name()` builds
  `t/<tenant>/w/<workload>/b/<bucket>/<id>` and refuses anything that could
  be a path trick. `r2test.FakeS3`: a hermetic, file-backed S3 server that
  checks every SigV4 signature.
- **hostd** (`hostd/r2.go`; one route line in `http.go`, one `Config.R2`
  field and default in `host.go`): `PUT|GET|DELETE /v1/r2/blob` on the
  local host API. hostd names the blob from the calling instance's tenant
  and workload (a replica `<w>--r<n>` uses `<w>`), only for buckets the
  deployment binds, and only IDs it generated; MD5 (the etag) and requested
  SHA digests are computed while streaming. The fs backend's root is added
  to the Seatbelt deny list. `internal/hostdrun` and `cmd/twd-hostd` pass
  the backend through (`hostdrun.Options.R2`).
- **bundle** (`hostd/bundle/r2.go`): `r2_buckets` → store bindings of kind
  `r2` named by `bucket_name` (default: the binding);
  `preview_bucket_name`/`jurisdiction` accepted, ignored.
- **gen** (`hostd/gen/r2.go`, `js/store-r2.js`, `js/store-r2-facet.js`):
  - the kind's index tables and journal entries (`put`/`del` by key, ckey
    `o:<key>`; multipart `mpu`/`part`/`mpx`), ops with `onlyIf` evaluated
    inside the conditional append (re-checked after a conflict), list with
    prefix/delimiter/cursor/startAfter/include in SQL, read-after-write
    catch-up with concurrent reads sharing one catch-up;
  - `R2Backend`: the R2 protocol (head/get/list via `cf-r2-request`;
    put/delete/multipart via the metadata-prefixed PUT body), R2's error
    codes, ranged reads across multipart parts, GC of unreferenced blobs
    after each write, and of a bucket's blobs when its store is deleted;
  - `R2FacetProxy` + a facet shim: a facet's `env.BUCKET` rebuilds
    `R2Object`/`R2ObjectBody` around an RPC stub.
  - **New shared plumbing:** `gen.StoreKindGen.FacetShim` (a facet-side
    module exporting `wrap(stub)`; `__twd_facet.js` wraps facet classes'
    env in a Proxy that applies it). KV/D1/Queues can use it for kinds
    whose results have methods.
- **CLI** (`cmd/twd/r2.go`, one line in `main.go`): `twd r2 bucket
  ls|create|rm`, `twd r2 object ls <bucket> [--prefix]` (folds the
  journal). No new API: buckets are `…/stores/r2/{bucket}`. `twd r2 object
  get|put` answers `not_supported` (bytes are only reachable from hosts).
- **Docs:** ADR draft, `docs/r2.md`, COMPAT rows `r2` and `r2-multipart`
  (⚠️) and the README rows, `r2_buckets` in the wrangler-config row and
  `docs/wrangler.md`, `docs/errors.md` (`not_supported`,
  `r2_backend_unavailable`, `r2_blob_missing`, `r2_too_large`).

## Tests (2026-11-15, macOS arm64)

- `go test ./hostd/ -run 'TestR2'` (real workerd, testcontrol): ok.
  - `TestR2Binding/{fs,s3}/{worker,facet}/{PutGetHeadMetadata,Ranges,OnlyIf,ListDelete,Multipart}`:
    20 subtests; both backends, both the Worker and a facet-mode object.
    Blobs land under `t/<tenant>/w/r2probe/b/{main,other}/` only; the fake
    S3 refused no signature.
  - `TestR2Streaming`: 1 GiB copied in-bucket (get body → put) and read
    back as a stream, sha256 checked. **hostd heap +1–3 MiB, workerd RSS
    +153 MiB** (bound in the test: 64 MiB / 384 MiB). Plus 8 MiB request
    bodies from the tailnet into a bucket and back, worker and facet.
  - `TestR2IndexSurvivesMove`: S3 backend; the workload is fenced and its
    DO storage deleted; every object (incl. multipart) reads back from the
    journal + S3. Then the bucket's store is deleted (as `twd r2 bucket
    rm`): the bucket reads empty, all its blobs are deleted from S3, and the
    next put starts a new incarnation.
- `go test ./hostd/r2/`: `TestSigV4AWSExample`, `TestBackends/{fs,s3}`
  (whole/ranged/suffix/empty reads, a short body stores nothing),
  `TestS3WrongCredentials`, `TestName`, `TestFlags`: ok.
- `go test ./hostd/gen/ -run TestGenerateR2`, `./hostd/bundle/ -run
  TestParseR2Buckets`, `./cmd/twd/ -run TestCLIR2`: ok.
- **microVM tier (vz, this Mac):** `cd e2e/vm && TWD_VZ_VMM=$PWD/../../bin/twd-vz
  TWD_VM_KERNEL=…/vz-assets-arm64/vmlinux TWD_VM_ROOTFS=…/rootfs.ext4 go test
  -tags e2evm -run TestE2EVMR2 .`: ok (10.5 s). Platform worker and Store
  object in the guest, bytes and journal over the guest NIC, S3 backend on
  the host; worker and facet; 64 MiB streamed in and out. Firecracker
  (Linux) not run here.
- **Across processes:** `go test -tags e2eplacement -run
  TestR2MoveBetweenHosts ./e2e/placement/` (`make e2e-placement`): ok
  (6.5 s). Real twd-mgmt + two twd-hostd processes with `-r2-backend s3`
  (credentials in their environment only); objects written on h1 are read
  on h2 after `replace` + `forceTakeover`; h2's conditional put sees h1's
  object; the S3 secret appears in no file under the run dir.
- `twd dev` (manual): put/get and a streamed body through a facet work
  (fs backend in its temp dir).
- `make lint`: ok. `make test` (all of `-race` Go + npm): ok, before and
  after merging origin/master (results below).

## Findings

- workerd's R2 binding protocol (pinned workerd): head/get/list are `GET`
  with the JSON in `cf-r2-request`; put/delete/multipart are `PUT` with a
  JSON prefix (`cf-r2-metadata-size`). Answers: metadata JSON then bytes;
  errors in `cf-r2-error` (`v4code`, lowercase c). A failed `get`
  precondition is a 412 carrying the metadata (workerd returns an
  `R2Object` without body); a failed `put` precondition is a 412 (→ null).
- workerd needs `storageClass` in the metadata (else `""`), takes `md5` as
  hex in the API but sends it base64 on the wire, and allows one hash per
  put.
- Leaving a PUT body unread when answering early (a failed precondition)
  makes the binding report "Network connection lost": drain it.
- Streaming without JS buffers needs the metadata prefix read with a BYOB
  reader (exactly n bytes) and the rest `pipeTo`'d natively. Pumping the
  value chunk by chunk through JS made workerd's RSS grow ~2× more (V8
  garbage, not buffering). The fixture's JS generator alone (no R2) adds
  ~150 MiB RSS per 512 MiB.
- A `ReadableStream` passed over RPC from a facet keeps its known length:
  `env.BUCKET.put(key, request.body)` works in a facet.
- `deploy --replace` of a workload with a DO class needs `forceTakeover`
  (its DO storage stays behind) even though its buckets move fine with the
  S3 backend.

## Gaps and next steps

- `twd r2 object get|put` (needs an mgmt endpoint proxied to the lease
  holder, like `twd logs`).
- Per-tenant backends with credentials in mgmt custody; an orphan-blob
  sweep; deleting a workload's blobs when the workload is deleted.
- Bucket mounts for containers (ADR-032) — documented follow-up.
- Abandoned multipart uploads are kept until aborted (no lifecycle).
- Firecracker run of `TestE2EVMR2` (Linux + KVM, `make e2e-vm`).
- Coordinate with KV (#15, branch `workers-kv-binding-issue-15`): both
  kinds touch `hostd/gen/stores.go` (this branch adds the `FacetShim`
  group) and the COMPAT `wrangler-config` row.
