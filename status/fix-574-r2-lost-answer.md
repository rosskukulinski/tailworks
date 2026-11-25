Last update: 2026-11-24 23:10 PT. Fix for #574 (found building the R2 data-plane chaos loop, #420).

## What

`R2Backend.#put` and `#uploadPart` (`hostd/gen/js/store-r2.js`) write the
blob, then the index row, and deleted the blob on any error from the index
write. When the journal append committed but its answer was lost
(`store_unavailable`), the index named a deleted blob: the key read
`r2_blob_missing` until overwritten. Now the blob is deleted only when the
index write certainly didn't land (`notWritten`: an R2 precondition or
checksum error, or the journal refusing it with `lease_lost`,
`store_conflict`, `bad_request`, `unknown_store` or `store_limit`).
Otherwise it is kept (an orphan at worst).

## Regression test

`hostd/r2lostanswer_test.go` `TestR2PutLostAnswer`: fakemgmt commits a
put's append and answers 503 (`LoseAppendAnswers`, new in
`hostd/fakemgmt/storelose.go`); the next get must return the new value.
On master it fails with `r2_blob_missing`; with the fix it passes.

## Tests

- `go test -count=1 -run TestR2PutLostAnswer ./hostd/`: fails before the fix, passes after.
- `go test -count=1 -run 'R2|Store' ./hostd/ ./hostd/gen/ ./hostd/r2/...`: pass.
- `make lint`, `make test`: see the PR.
