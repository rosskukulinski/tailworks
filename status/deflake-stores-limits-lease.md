Last update: 2026-11-23 11:30 PT. TestStoresLimits deflaked (lease_lost after 48 s on CI).

## Problem

`mgmt TestStoresLimits` failed on the release PR #259 (CI run 45238488831)
after 48 s with `409 lease_lost` on its 4 MiB journal append. The deploy's
workload lease lives `NamespaceLeaseTTL` (45 s) and only heartbeats renew
it. The store tests never heartbeat, so a test that runs past 45 s loses its
lease.

## Why it took 48 s

A CPU profile (`-race`, idle Mac) showed 15 s total, almost all of it in
kin-openapi's `format: byte` check. It's a regexp
(`openapi3.FormatOfStringByte`) that Go runs on its NFA matcher. The spec
validator middleware ran it on each multi-MiB base64 request and response
body. Under `-race` on a loaded runner that came to tens of seconds.

## Fix

- `mgmt/api/v1/byteformat.go`: the Validator checks `format: byte` in one
  pass, accepting the same strings as the regexp. Tests:
  `TestValidBase64CharsMatchesRegexp` (table plus 20k random strings against
  the regexp) and `TestValidatorByteFormat`.
- `mgmt/leasekeeper_test.go`: `keepLease` heartbeats a host's lease the way
  hostd does (every `NamespaceLeaseTTL/9`). `newStoreEnv` uses it, which
  covers every store test (`stores_test.go`, `storedegraded_test.go`,
  `quota_test.go`, `kv_test.go`), not just Limits.

No other mgmt test is close. In a full `-race` run of `./mgmt` before the
fix, TestStoresLimits took 19.4 s and the next slowest took 6.9 s.

## Results

Reproduced with a `-race` test binary, `-test.cpu=1`, 72 `yes` CPU burners
on an 18-core Mac (`/tmp/deflake/load.sh`), `-run TestStoresLimits$`:

| build | runs | result | time per run |
| --- | --- | --- | --- |
| master | 3 | 3 FAIL `lease_lost` at stores_test.go:212 | 50.8 to 52.1 s |
| heartbeat only | 3 | 3 PASS | 72.0 to 72.3 s |
| both fixes | 10 | 10 PASS | 11.8 to 12.9 s |

Idle, `-race`: 15.0 s before, 2.7 s after.

## Commands

- `make lint`: ok
- `make changes-check`: ok (no upgrade-relevant paths)
- `go test ./mgmt/api/v1/ -run 'Base64|ByteFormat'`: ok
- `make test` (mbp runner, after merging origin/master at 59e8abad): exit 0 (mgmt 259.6 s, mgmt/api/v1 ok)
