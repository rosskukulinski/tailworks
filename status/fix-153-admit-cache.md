Last update: 2026-11-21 PT. Fix #153 (ledger H19 / F12 admit part): no create admission outlives a kill.

# fix-153-admit-cache

## Root cause

A create admission is standing permission to mint a node for a name.
hostd's `ensure` takes one whatever path reaches it: the admitted request,
but also a plain DO's constructor reached through the isolate's admit
cache, a raw binding, or a stub kept from before the kill. Three kinds of
admission survived a kill and a purge:

1. **Pending admissions (`h.admits`).** `killLocal` did not drop them, and
   `admit` recorded new ones for a killed name, because a kill removes the
   object from `byKey` and `admitCreate` saw "no object". An admission
   recorded before the kill and never consumed (the name was then created
   by an object's spawn, which takes none), or recorded between the kill
   and the purge (the call got 410 from the kill marker and did not
   consume it), was taken after the purge by the next constructor `ensure`.
   mgmt had forgotten the object, so it minted a new one.
2. **public.create records (`pubCreates`).** `killLocal` forgot them only
   for `ActingFor == "public"` objects. A plain DO's constructor `ensure`
   names no principal, so a publicly created plain-DO object kept its
   record forever (on disk), and `ensure` treats a record as an admission.
   Its `maxObjects` slot also stayed taken.
3. **The runtime's isolate cache (`admitted` in `runtime.ts`).** It kept
   every successful admit, including one-shot "may create" answers, with no
   expiry and no eviction on kill. A later call skipped `admit` and went
   straight to the DO, whose constructor called `ensure` even when the
   object was killed.

A tenant purge (#107) also left the tenant's pending admissions in memory
and its public.create records in `public-creates.json`.

Confirmed with failing tests before the fix: `TestRT3_H19_AdmitCacheAfterKill`
(mgmt was asked to create the killed, purged name in all three subtests),
`TestTenantPurgeDropsAdmissions` (public.create record left), and all three
tests in `admitcache.test.js`.

## Fix

- `hostd/admitkill.go` (new): `dropAdmissionLocked`, `killedLocked`
  (a killed tombstone of the key on this host), `dropTenantAdmissions`,
  `publicCreates.forgetTenant`.
- `hostd/objects.go` `killLocal`: drop the name's pending admission at the
  same time as it leaves `byKey` (before the in-process kill); forget the
  public.create record for every classed object, not only `ActingFor
  "public"`.
- `hostd/public.go` `admitCreate` / `admitPublic`: a name killed on this
  host answers `410 killed` and records nothing until it is purged.
- `hostd/tenantpurge.go`: one call, `dropTenantAdmissions(id)`.
- `js/packages/tailnet/src/runtime.ts` (and the regenerated
  `hostd/gen/js/tw-rt.js`): the admit cache keeps only `existing: true`
  answers, for 60 s; a plain DO's `__tw_kill` evicts its entry; a plain DO
  whose storage holds the kill marker calls no `ensure` from its
  constructor.
- `docs/create-admission.md`: a paragraph on killed names.
- `security/LEDGER.md`: H19 fixed; F12 admit part done (the cron part, #152,
  stays queued).

Works with #94's `killRun` (the admission is dropped inside the first
run's locked section; later callers wait as before) and is independent of
#133's `nodeGen`.

## Tests

- `go test ./hostd -run 'TestRT3_H19|TestTenantPurgeDropsAdmissions' -count=1`:
  failed on master (4 failures), pass after.
- `node --test test/admitcache.test.js` in `js/packages/tailnet`: 3/3 failed
  against master's runtime, pass after; `node --test test/*.test.js` 35/35.
- `make changes-check`: ok. `make lint`: ok.
- First `make test` run found two regressions in this change, both fixed:
  `TestPurgeDeletedTenant` panicked (a Host without `pubCreates`; now
  nil-safe), and `TestHostd/KillFailsFast` got 500 instead of 410 through
  the workload (admit's new `killed` answer was not mapped to a response;
  `asResponse` now maps a typed `killed`). It also hit flakes in
  `TestCLISingleProcess` (kill 1.35 s on a loaded machine),
  `mgmt.TestWarmKeyExpiry` and a `TestLockTemplate` hang. Each passed when
  rerun alone.
- After merging origin/master (0d6e0cf): `make lint` ok, `make test` ok
  (Go and JS, exit 0).
- Not run: `make test-pg` (no mgmt store change). `make e2e-locks` not run
  either: kill itself is unchanged; only admit's answer for a killed name.

## Gaps

- A facet's kill runs in the platform worker, not in the tenant isolate, so
  the isolate's cache entry for a killed facet is not evicted. It is bounded
  by the 60 s TTL and is a false refusal, not a mint: the Host DO asks hostd
  before any tenant code runs.
- A mgmt-only purge (`DELETE /v1/tenants/{id}/objects` without hostd's
  `POST /v1/purge`) leaves hostd's tombstone, so `admit` keeps answering
  `killed` on that host until hostd purges too. `tw down --purge` does both.
- RPC methods of a killed plain DO are not checked against the kill marker
  (only `fetch` and `__tw_intercept` are). That is outside this issue.
