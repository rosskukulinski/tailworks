Last update: 2026-11-21 PT. #137: TestWarmKeyExpiry now waits on the revoke records and checks the drained keys, not an empty pool.

# deflake-137

## Root cause (two test races, no warmkeys.go bug)

`TestWarmKeyExpiry` failed two ways under load. Both were the test reading
state before the thing it asserts had finished, or asserting something the
pool doesn't promise.

1. **"after drain: map[<tenant> tag:crm|persistent:1]"**: `DrainWarmKeys`
   revokes the keys that are warm when it runs. The pool loop keeps running
   (50 ms tick in the test), and the deployment is under an hour old, so
   `poolTargets` still wants one key per class. As soon as a throttle token
   is free, a refill mints a fresh key. The test then read `WarmKeyStats`
   and expected no keys for the tenant. That holds only if no refill lands
   between the drain and the read. Drain itself is correct: it takes each
   key out of the pool with the conditional `TakeWarmKey` (oldest
   `expires_at` first, so the listed keys go before any newer refill), then
   revokes it.
2. **"2 revoke records for 3 expired keys"**: `expireWarm` writes each key's
   `revoke` audit record after its `DeleteKey` returns. The test waited
   until the fake API had seen every delete, then `waitWarm` for a
   replacement, then counted records. The replacement doesn't order after
   the audit writes: `refillPool` counts only keys with more than
   `WarmKeyTakeMargin` left, so once the test moves the clock past the take
   window, the same tick can mint a replacement before the next tick
   expires the old keys. So the count could run while the last record was
   still being written.

## Evidence

- Natural repro on Postgres 17 (private container, port 55491), old test:
  `TW_TEST_API_POSTGRES=1 go test -race -run 'TestWarmKeyExpiry$' -count=15 ./mgmt/`
  gave 1 FAIL with the CI message (`after drain: map[tn_… tag:crm|persistent:1]`), 14 PASS.
  SQLite, `-count=20`: 20 PASS (it needs load to line up).
- Deterministic repro of (1): a 3 s sleep between `DrainWarmKeys` and the
  stats read failed 3/3 with all three pools refilled.
- (2) follows from the code order above. A 1–2 s sleep between
  `revokeWarm` and `m.audit` alone doesn't trip the old test, because it also
  needs a refill to land before the expiry tick. That interleaving is what the CI
  failure under load shows.

## Fix (test only, `mgmt/warmkeys_test.go`)

- After the API deletes, poll `ListAudit` (chain mgmt, this tenant, kind
  `revoke`) until every expired key's ID has a record: the real completion
  signal, and stricter than the old count (it matches key IDs).
- Drain: list the tenant's warm keys right before `DrainWarmKeys`, then
  check that none of them is still warm and each was deleted at the API.
  Keys refilled after the drain are allowed.
- With both probes in place (3 s after drain, 1 s before each revoke
  record), the new test passed 3/3.

## Tests

- Postgres 17, new test: `-race -run 'TestWarmKeyExpiry$' -count=40`: 40/40 PASS.
- SQLite, before the throttle fix below: `-race -run TestWarmKey -count=15`
  hit a different flake once, `TestWarmKeyRefillRespectsThrottle`: "refill
  after 3.79s, faster than the 10/min budget allows".
- SQLite, after both fixes: `-race -run TestWarmKey -count=8`: 24/24 PASS.
- `make lint`: ok. `make test`: ok. `make test-pg` (with `TW_TEST_POSTGRES_DSN`
  pointing at a private postgres:17-alpine container): ok, ok.

## Also fixed: TestWarmKeyRefillRespectsThrottle timed from the wrong moment

It measured the refill from the take (`t0`), but the take spends no token.
The seed mint spent the one token. The next token is due about 6 s after
the seed mint, so when the test's 1 s sleep and the object create pushed the
take more than 2 s past the seed mint, a correct refill came "too fast". It now
measures from when the seed key first shows up (just after its CreateKey).

## Gaps / notes

- `expireWarm` revokes at the API and then writes the audit record. If mgmt
  dies between the two, that revoke has no record. An intent-first record
  (`m.intend`/`recordIntent`, as mints use) would close that. It's out of
  scope here.
- `Purge`'s comment says the pool "refills on the next create or deploy".
  In fact it refills on the next pool tick while the deployment is under an
  hour old.
