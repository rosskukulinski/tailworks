# Red-team Pass 3 and its fixes (branch `red-team-security-review-loop`)

Last update: 2026-11-14 23:30 PT. **Red-team Pass 3 fixes F1 and F4** (`security/RED-TEAM.md` Pass 3, `security/LEDGER.md`). F1: a host's writes to mgmt are bound to the leases it holds: object create/spawn, `ready` (holder always, epoch when sent; hostd always sends it), pool-node transfer, deployment status, and audit ingest (signature required, object must belong to a workload the host has served). F4: object creation is admitted, never implied (`docs/create-admission.md`): hostd stamps a create ticket on tailnet requests and cron runs, the runtime's guarded env presents it, the Host DO no longer reads `request.cf` to decide "public", raw bindings and `__TWD` get `create_not_admitted`, `spawn.principal` can no longer name a principal. Cron runs are dispatched as a fetch with `cf.twd.kind=scheduled` (workerd's native `scheduled()` drops extra controller fields). `make test`-equivalent suites green: mgmt, hostd (incl. `TestPublicIngress` §3b bypass battery), hostd/gen, cmd/twd, netd, JS. New tests: `mgmt.TestRT3_HostWritesAreLeaseBound` (fails on `4f23a96`: 202 where 409 is required), `gen.TestCreateTicket`, public ingress §3b.

## Where things are

- Findings: `security/RED-TEAM.md` (Pass 3 section, with the fix-status table).
- The standing loop: `security/LOOP.md` (`/loop Follow security/LOOP.md`) and
  its ledger `security/LEDGER.md` (hypotheses, claims register, fix queue;
  `FIX_MODE: propose`).
- Design note for F4: `docs/create-admission.md`.

## Tests (commands that produced the results above)

- `go test ./...` (hermetic; mgmt, hostd, hostd/gen, cmd/twd, netd, internal,
  security/attacks): PASS.
- `node --test js/packages/tailnet/test/*.test.js`: 21/21; `npm test -w @twd/mcp`: 5/5.
- `mgmt.TestRT3_HostWritesAreLeaseBound` against unmodified `4f23a96` (temporary
  worktree): FAIL as intended (`POST /v1/objects: got 202, want 409 lease_lost`).
- gofmt, `go vet` across all build tags, `scripts/check-secrets.sh`: clean.

## Known gaps / next

- The extended `TestPublicIngress` §3b was not re-run against unmodified master
  (the temporary worktree could not build the fixture); the bypass was shown
  hermetically in Pass 3 and the assertions pass after the fix.
- Open, highest first (`security/LEDGER.md`): F2 audit-queue overflow from an
  anonymous flood (RT3-03); F5 in-process `request.cf` forgery (RT3-08); per-IP
  connection cap (RT3-09); cross-tenant public-name squatting (RT3-10); uffd
  region validation (RT3-11); terminal escapes in `twd audit` (RT3-13).
