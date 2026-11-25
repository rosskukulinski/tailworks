Last update: 2026-11-24 22:45 PT. Fix for #562 (found by the R2 data-plane chaos loop, #420).

## What

`Store.commit()` (`hostd/gen/js/store.js`) acknowledged a write whose
entries function returned `[]` (nothing to write, decided on the local
copy) without contacting the journal. Only appends are checked against
the journal's head, so a stale copy (a replica behind another replica's
write, or a host cut off from mgmt) could acknowledge a delete that never
happened. Now an empty write catches up first and re-runs the entries
function on the caught-up copy; if the journal can't be reached the
write fails.

Applies to every store kind: R2 delete / failed `onlyIf` put / abort of
an unknown upload, KV delete of an absent key, D1 writes that change no
row.

## Regression test

`hostd/storenoop_test.go` `TestStoreNoopWriteChecksJournal`: another
writer appends `c` to the journal; a delete-if-present of `c` on the
stale copy must append and remove it; with the journal down a no-op
delete must fail `store_unavailable`. Fails on master (0 appends, `c`
still there), passes with the fix. The probe store kind
(`hostd/hostdtest/storeprobe.go`) gained a `delIf` op for it.

## Tests

- `go test -count=1 -run TestStoreNoopWriteChecksJournal ./hostd/`: pass; fails with master's store.js (verified by swapping it back).
- `go test -count=1 -run 'Store|KV|D1|R2|Kv' ./hostd/ ./hostd/gen/`: pass.
- `make lint`: pass. `make changes-check`: ok (fragment added anyway: `changes/fix-562-noop-write.md`).
- `make test`: see the PR.
