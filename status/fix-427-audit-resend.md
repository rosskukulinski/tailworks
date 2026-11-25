Last update: 2026-11-25 11:30 PT. mgmt stores a re-sent new-segment audit batch once (#427).

# fix-427-audit-resend

## Cause

`TestAuditChainAcrossRestart/clean_restart` reproduced 2 times in about
170 runs under load (10 at once plus `yes` hogs), with the chain records
dumped on failure:

```
rec seq=1 kind=kill prev= hash=f6b529e8 obj=probe-q4f5w3rglk
rec seq=2 kind=kill prev= hash=f6b529e8 obj=probe-q4f5w3rglk
```

The host chained one record and mgmt stored it twice. The resume logic
(#377) worked: the saved head matched mgmt's, and the next record linked
to it. hostd re-sends a batch whose POST failed, and a POST can fail after
mgmt stored it. Here the heartbeat tick's flush was in flight when `Close`
cancelled `h.ctx`, and `Close`'s own flush sent the record again.
`IngestAudit` skips the stored part of a re-sent batch only when the
batch's first `prevHash` is not "". A batch that starts the chain (or a
new segment) was appended a second time, and the chain broke at the copy.
That is the CI failure (`Records:2 BrokenAt:2`), and it could happen in
production the same way.

## Fix

`IngestAudit` looks for mgmt's head in any batch that doesn't link to it,
including one that starts with `prevHash` "". A batch that has the head
is a re-send: only the records after the head are stored. A new segment
without the head is stored as before, and verification still reports it.

## Tests

`TestAuditResendIsIdempotent` (mgmt): it sends the first record twice, a
batch that overlaps the stored records, and a new segment twice. Without
the fix it fails with the CI signature (`Records:2 BrokenAt:2`).
- `make lint` and `make test` (macOS, local, after merging origin/master): ok.
- `TestAuditChainAcrossRestart` before the fix: 2 failures in about 170 stress runs, both with the duplicated first record. With the fix, under the same load: 95 runs of all three subtests passed. One binary crashed on an unrelated hostd bug ("close of closed channel" in `closeOnce`), which PR #632 fixes.
- `make e2e-multimgmt`: could not run. Another agent's run on this machine held the shared `tw-mm-pg` container and Postgres, so every scenario failed at setup (a container name conflict, or "replicas sharing a store must share custody").
