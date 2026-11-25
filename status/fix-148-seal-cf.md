Last update: 2026-11-21 PT. Fix #148 (ledger H08 / F5): request.cf's caller is sealed by hostd and verified before tenant code or audit uses it.

# fix-148-seal-cf

## Pre-merge fix-up (2026-11-21, approved by Ross)

Merged origin/master (#214, #219) with no conflicts. `security/RED-TEAM.md`'s
RT3-08 fix-status row now says Fixed (F4, F5). Results: `make lint` PASS,
`make test-sdk` PASS, `make test` (mbp runner) PASS,
`TestRT3_H08_ForgedPrincipal` and `TestHostd/FacetHostInternalsNotRPC` PASS.

## Merge with #215 / #217 (2026-11-21)

One conflict, in `hostd/gen/js/platform.js`: the facet Host's RPC Proxy.
#215 replaced `self.__facet()` with the module function `hostFacet(self,
cls)`, and this branch seals `Request` arguments there. Both are kept:
`hostFacet(self, cls)[p](...await sealed args)`. `Host_<Class>.fetch`
merged cleanly: the seal check runs before `principalOf`, then
`hostAdmitted`/`hostFacet`. The seal helpers (`verifiedCaller`,
`sealedCf`, `sealedRequest`) are module functions, not Host methods, so
they are not reachable over RPC (#215's rule). The seal MAC's domain
(`tw-caller-seal-v1\n…`) is distinct from `hostCap`'s
(`tw-host-internal-v1`). Results after the merge:

- `go test ./hostd/ -run 'TestRT3_H08_ForgedPrincipal|TestHostd/FacetHostInternalsNotRPC' -v`: both PASS.
- `go test ./hostd/gen/`: PASS.
- `make lint`: PASS. `make test-sdk`: PASS.
- `make test` (mbp runner): PASS, including `TestCLIEndToEnd` (#217's
  lease_lost retry fixed the #210 failure seen earlier).

## Problem

netd puts the WhoIs caller in `request.cf.tailscale.caller`. Once tenant
code forwards a request in-process (a Durable Object stub `fetch`, or a
`Request` passed as an RPC argument), `cf` is whatever the sender says.
The receiving object's `getCaller()`/`caps()` returned a forged caller,
and the platform's `principalOf(req)` in `Host_<Class>` turned it into the
object's `actingFor`: hostd wrote a `source: host` `principal` record and
stamped every later `tenant_note` with the forged principal.

Confirmed with the failing regression test before the fix (commit
`hostd: TestRT3_H08_ForgedPrincipal (failing …)`). A facet object and a
plain DO both reported `nodeId: nFORGED`, `user: ceo@victim.example` and
the forged `caps()`, for a forged blob, a swapped caller and an RPC
`Request` argument. Audit had two `tenant_note` records and one
`source: host` `principal` record with `actingFor: ceo@victim.example`.

## Fix (design)

- **Seal (hostd, `hostd/sealcf.go`, `hostd/gen/seal.go`).** For every
  tailnet (non-public) request it forwards to workerd, hostd adds
  `cf.tailscale.seal = {v: 1, exp, caller, mac}`. `caller` is the caller's
  exact JSON, `exp` is now + 5 min (ms), and `mac` is
  `hex(HMAC-SHA256(MAC_KEY, "tw-caller-seal-v1\n" + exp + "\n" + caller))`.
  `MAC_KEY` is the existing per-instance root key: the same key that
  derives per-object egress keys and signs create tickets. The seal's
  domain separates it from both. There is no new secret and no new
  distribution path.
- **Verify (platform, `hostd/gen/js/platform.js`).** `verifiedCaller`
  checks the version, the expiry window and the HMAC (WebCrypto `verify`,
  constant time). `sealedRequest` rebuilds the request so that
  `cf.tailscale.caller` is the parsed sealed copy, or so that it has no
  caller and no seal. It runs in `Host_<Class>.fetch` before `principalOf`
  and before the facet sees the request, and on every `Request` argument of
  a facet RPC call.
- **Verify (runtime, `js/packages/tailnet/src/seal.ts`).** The tenant
  service's entry points (the Worker's `fetch`, both handler shapes, and
  plain DO `fetch`) run in the tenant isolate, which must never hold the
  key. They ask the platform (TailnetApi op `unseal`, a verify-only
  oracle) and rewrite `cf` the same way. Verified seals are cached per
  isolate by MAC (1024 entries, until `exp`).
- The tenant's own bundled `@tailworks/tailnet` (any version) still reads
  `cf.tailscale.caller`. That is now the verified copy, so `getCaller()` is
  unchanged, synchronous, and needs no SDK upgrade.
- `principal`/`actingFor` in tenant op args: already closed by F4
  (`actingFor`/`public` stripped in `TailnetApi.call`; `spawn.principal` is
  `inherit`/`none` only). Nothing else changed there.

## Limits (documented in docs/security-model.md Known gaps)

- **Replay of the same real caller.** A seal travels with `cf` so that the
  documented pattern (forward the request you received) keeps working.
  Tenant code can therefore replay a caller it was really sent, to any
  object in the same workload instance, until `exp`. It cannot make or
  alter a caller. Seals are per instance: they don't verify in another
  workload, or after a redeploy.
- A `cf` passed as plain data (not on a `Request`) isn't checked.
- RPC `Request` arguments to plain DOs aren't checked (only facets). Plain
  DO RPC is being reworked in #211, and this branch stays out of it.
- Plain DOs share the Worker's isolate: code that rewrites the isolate's
  built-ins can defeat any in-isolate check. Facets get their own isolate.
- `cf.tailscale.requester` (public) is not sealed. Forging it only
  downgrades a request (no caller).

## Upgrade / mixed versions

`platform.js` and `tw-rt.js` are generated per instance by the hostd that
starts it, so the hostd that seals and the platform/runtime that verify are
always the same version. A workerd started by an older hostd ignores the
extra `seal` field (the old behaviour). A tenant's bundled SDK version
doesn't matter. `make changes-check` calls nothing here upgrade-relevant.
The fragment is `changes/fix-148-seal-cf.md` (`kind: security`,
`upgrade: []`).

## Tests

- `go test ./hostd/ -run TestRT3_H08_ForgedPrincipal`: FAIL before the fix
  (output above), PASS after.
- `go test ./hostd/gen/ -run TestCallerSeal`: PASS.
- `go test ./hostd/gen/`: PASS (gen tests now send a sealed caller, as hostd does).
- `make lint`: PASS (after merging origin/master with #213).
- `make test` (mbp runner, twice): all PASS except `cmd/tw TestCLIEndToEnd`.
  Both times the agent redeploy after a supervisor restart timed out
  waiting for its node (`identity_pending … node minted`). It passes
  locally (twice). Known mbp flake #210 (a lease_lost epoch race in redeploy).
- `make templates-typecheck test-sdk`: PASS.
- `e2e-upgrade`: not run (changes-check: not upgrade-relevant; see above).
