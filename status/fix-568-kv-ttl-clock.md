Last update: 2026-11-24 23:15 PT. KV TTLs are stamped on the store object's clock (#568); regression test under a skewed workerd clock.

# fix-568-kv-ttl-clock

Found by e2e/dataplane's new skew fault (#415 H3, branch
`dataplane-415-finish`): after the host's wall clock stepped back, keys put
with `expirationTtl: 60` were missing 20 s later (12 in one run).

## Cause

`parsePut` (`hostd/gen/js/kv.js`) ran in the stateless `KVBackend` and
turned the TTL into an absolute expiry on that isolate's `Date.now()`. The
store Durable Object checks expiry on its own `Date.now()`, which never goes
back (workerd holds it at the latest value it returned). After a step back
larger than the TTL, a new key was already expired on the object's clock:
acknowledged, then invisible, then deleted by the expiry alarm.

## Fix

The backend passes the TTL to the store's `put` op, which sets
`x = nowSec() + ttl` on the object's clock. Absolute `expiration` is
unchanged.

## Tests

- `hostd:TestKVTTLAfterClockStepBack` (new, Linux, needs `cc`): workerd runs
  under `internal/wallclock/testdata/skewclock.c` through a wrapper script
  (`hostdtest.Options.Workerd`), the clock steps +5 min and back, and a 60 s
  TTL key is read right after the put. Linux arm64 (Lima): fails before the
  fix (`null, want "v"`), passes after. Skips on macOS.
- `go test -run 'TestWorkersKV' ./hostd/`: pass (macOS and Linux).
- `make lint`: pass. `make test` (macOS): pass.

Based on `dp-skew-hook` (#564), which adds the preload library.
