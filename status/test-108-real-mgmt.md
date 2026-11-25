Last update: 2026-11-21 PT. #108: the hostd store conformance tests now also run against the real mgmt under `make test`; fakemgmt has a route drift test.

# test-108-real-mgmt

## What's built

- **`hostdtest.BackendReal`** (`hostd/hostdtest/realmgmt.go`). `Options.Backend`
  picks the mgmt behind an Env: `fakemgmt` (default, unchanged) or `real`.
  The real backend is package `mgmt` in the test process: SQLite store, file
  custody, and a minimal fake Tailscale API (OAuth exchange, auth keys
  testcontrol accepts, deletes, policy, devices). hostd talks to it through
  `internal/inproc`, as the host embedded in single-process `tw up` does,
  and mgmt pushes through `Host.PushHandler` (`SetInProcessPush`, push URL
  `inproc://`). The CLI side uses mgmt's unix socket behind `LocalHandler`.
  Both sides go through the spec validator, as with fakemgmt. mgmt's state
  lives in its own temp dir, outside hostd's, so tests that walk hostd's
  directories (secrets) see only hostd's files.
- **`Env.Hooks`**: the test controls the store tests used on `e.FM`
  (`StoreAppends`, `SecretGrants`, `SetHeartbeatsDown`, `QueueClaims`,
  `QueueBacklog`). fakemgmt implements them itself. For the real mgmt they
  are a middleware on the host's transport, plus `GET /v1/tenants/{id}/queues/{q}`
  for the backlog.
- **Parameterized bodies**: `TestWorkersKV`, `TestD1`, `TestR2Binding`,
  `TestQueues` and `TestWorkerSecrets` are now one-line wrappers over
  `testX(t, backend)`. **`TestRealMgmtStores`** (`hostd/realmgmt_test.go`)
  runs all five bodies with `BackendReal`, as parallel subtests.
- **`TestRouteDrift`** (`hostd/fakemgmt/drift_test.go`) reads fakemgmt's
  route table (its `HandleFunc` patterns) and fails when:
  - fakemgmt serves a route that isn't in `api/openapi.yaml`;
  - a spec operation a host may call (`x-tw-role` includes `host`) is
    missing from fakemgmt and isn't listed, with a reason, in
    `hostRoutesNotFaked`; or a listed route is now served, or no longer in
    the spec;
  - fakemgmt's answer on any of its routes (a probe with placeholder IDs, as
    an admin and as host h1) has a status or body the spec doesn't declare.

## Divergences found and fixed (in fakemgmt)

- `GET /v1/hosts/{id}/sandbox-deny` was missing. hostd's seatbelt profile
  quietly fell back to the older-mgmt path (`GET /v1/tenants`). It is now
  served as mgmt serves it (`hostd/fakemgmt/sandboxdeny.go`).
- `PUT /v1/hosts/{id}` answered `tiers: null` for a registration without
  tiers. The spec's `Host.tiers` is a non-nullable array, so it's now `[]`.

The five store bodies passed against the real mgmt on the first run, with
no spec violations. No real-mgmt bug was found, so no bug was filed.

## Out of scope (still open on #108)

- An interface for the client subset hostd uses, so fakemgmt can shrink.
- Moving e2e-placement's hermetic parts into `make test`.
- `hostdtest.StartHost` (a second host) and the registry wiring
  (`Fake.RegistryDir`, `Images`) are fakemgmt-only. `BackendReal` fails
  fast if they are asked for.

## Cost

`TestRealMgmtStores` adds about **33 s wall** to `make test` (go test -race,
M-series Mac). The five bodies run in parallel, so the cost is the slowest
one: Queues, about 31 s of fixed waits (visibility, retry delays). Run
serially, the five would take about 44 s without -race. `TestRouteDrift`
takes 0.1 s.

## Tests run

- `go test -race -count=3 -run '^TestRealMgmtStores$' ./hostd/`: ok (101 s, 3 runs).
- `go test -count=1 -run '^(TestWorkersKV|TestR2Binding|TestD1|TestWorkerSecrets|TestQueues)$' ./hostd/` (fakemgmt): ok.
- `go test -count=1 -run TestRouteDrift ./hostd/fakemgmt/`: ok. Before the
  fakemgmt fixes it failed on the eight unlisted host routes and on `tiers: null`.
- Sanity check that the real backend is really in use: I made the real
  hooks' `SecretGrants` off by 100, and `TestRealMgmtStores/Secrets` failed
  with `secret grants 101`. Then I reverted it.
- `make lint`: ok.
- `make test` (mbp runner, `mbp-test.sh test`): exit 0. `hostd` passed in
  336.6 s and `hostd/fakemgmt` in 8.3 s.
- After merging origin/master (#220, CI only): `make lint` ok.
