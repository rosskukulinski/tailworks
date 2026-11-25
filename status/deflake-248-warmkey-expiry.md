# deflake-248-warmkey-expiry (issue #248)

`mgmt.TestWarmKeyExpiry` failed in CI on docs-only PR #247 with
`warmkeys_test.go:299: custody copy of revoked key k3 kept`.

## Root cause: a test race, not a product leak

`revokeWarm` (mgmt/warmkeys.go) calls `DeleteKey` at the API, then deletes
the custody copy, then the row. The test waited until the fake API had
seen every DELETE, then checked custody right away. The fake records the
delete before it answers, so the last key revoked (always k3, the last in
`before`) could still have its custody copy for a moment. The product
always deletes the copy after a successful revoke, so nothing leaks.

## Fix

The test now waits for the revoked keys' rows to go (the last step of
`revokeWarm`, so a real completion signal), with a deadline, and only then
requires custody to be clean. A copy that is never deleted still fails
the test.

## Tests

Stress: the `-race` test binary, 16 parallel copies of
`-test.run '^TestWarmKeyExpiry$' -test.count=50 -test.cpu=1` (800 runs per
round):

- before: 2 failures in 800 runs, both `custody copy of revoked key k3 kept`
  (6 copies × 50 without `-test.cpu=1`: 0 in 300, too little load).
- after: 0 in 800, twice (0 in 1600).

- `make lint`: ok.
- `make test` on the mbp runner: ok.
