---
kind: security
upgrade: [protocol]
---
The host audit chain survives a flood (red-team H03, #68). Before, a flood
of tenant `audit()` notes or of public requests to unlisted paths, while
mgmt was slow or unreachable, made hostd drop the oldest queued records.
Those records were already chained, so mgmt refused every later batch and
the host's chain stayed broken for good. Now hostd never drops a chained
record: host records spill to `<data-dir>/audit-spill.jsonl` once 10 000
wait in memory, and are flushed in order, in batches. Records that may be
shed are refused before they are chained and counted in a new in-chain
`audit_dropped` record, so the chain still verifies and the gap shows in
`tw audit`. Those records are tenant notes over a per-object rate (10/s,
burst 200) or sent while the queue is backed up, and `public_request`
records once the spill reaches 256 MiB. Public `404 not_public` and
`400 bad_request` refusals are no longer one `public_request` record each.
They are counted in the per-minute `public_rejected` summary
(`notPublic`, `badRequest`).

`audit_dropped` is a new `AuditKind`. An older mgmt stores it like any host
record.
