Last update: 2026-11-17 PT. Dependency hygiene (issue #112): Dependabot for Go/npm, staticcheck + govulncheck in `make lint`, tailscale.com pin documented, advisory policy.

# deps-112-hygiene

## What changed

- **Dependabot** (`.github/dependabot.yml`): gomod (`/`, `/tools`), npm
  (`/` with every `js/packages/*` workspace, `/e2e/node`, `/e2e/vitest`) and
  github-actions. Weekly on Monday, minor+patch grouped into one PR per
  ecosystem, at most 5 open PRs. tailscale.com + `github.com/tailscale/*`
  and workerd each get their own group, because they need more than CI
  (e2e-placement/e2e-real; a `workerd` fragment). The templates'
  `package.json` files are left out: `scripts/npm-versions.mjs` owns their
  `@tailworks/*` ranges and they have no lockfile.
- **Lint tools in their own module** (`tools/go.mod`, `tool` directives for
  staticcheck v0.8.2 and govulncheck v1.10.0), so their x/tools etc. never
  move what the main module builds against. `make lint-tools` builds them
  into `bin/lint/`.
- **`make staticcheck`**: blocking, in `make lint`. Runs with `-matrix` for
  linux and darwin (CGO off) so platform-only code isn't reported unused and
  a Mac reports what CI reports (`scripts/staticcheck.sh`). Packages that
  build on one OS only (today `hostd/vm/helper`) are checked for that OS
  alone, since -matrix fails on a package with no files for one config;
  the first CI run failed on exactly that. `cmd/tw-vz` (cgo) is left out;
  it's clean when checked natively on a Mac.
- **`make vulncheck`**: govulncheck. In `make lint` as a non-blocking step
  (`-@$(MAKE) vulncheck`, TODO(#146)) because the only remaining finding
  needs the go1.27.4 toolchain.
- **tailscale.com pin** documented in a go.mod comment (not bumped).
- **Advisory policy**: `docs/RELEASING.md` "Upstream advisories" (sources
  per upstream, triage in 2 business days, critical/high patch within 7
  days, what a security patch release may bump); a pointer in SECURITY.md.
- **kin-openapi v0.142.0 → v0.149.0** (only that line in go.mod).

## Findings

### Why tailscale.com is on an untagged commit

`git log -L` on go.mod: the pin dates from the first commit (406f9cf, M0).
STATUS.md "M0" says it's "exactly `.refs/tailscale` 5030108", and the RFC's
"Prototype results" say the 2026-11-07 spikes ran on tailscale.com main
5030108. So it was the commit the prototypes used, not a feature need.

Checked with a scratch `-modfile`: the tree builds and vets against tagged
v1.104.5. v1.104.6 and v1.104.7 need go >= 1.27.4 (blocked on #146). But
v1.104.x branched from main on 2026-09-30, so any 1.104 tag rolls back ~5
weeks of main underneath netd's use of tsnet/ipn/gVisor internals.
Not bumped. Un-pin condition (in go.mod): the first tag cut from main after
5030108 (v1.106.0), with `make lint test e2e-placement` and `make e2e-real`.
hujson, peercred and gvisor have no tags at all; they follow tailscale.com.

### govulncheck on master (before this branch)

- 8 reachable kin-openapi advisories (GO-2026-6112, -6783, -6785, -6789,
  -6790, -6795, -6800, -6804), via `mgmt/api/v1/validate.go` (request and
  response validation) and `api/render`. Fixed by v0.149.0, which builds
  with no code change; `make api-check` shows no generated-code change.
- GO-2026-6859 (net/http MaxBytesReader header race, HTTP/1 server crash):
  fixed in go1.27.4. Not fixed here: with `GOTOOLCHAIN=local` every machine
  must install go1.27.4 first. Opened **#146** (security, bug).
- Module-level only (not imported): x/crypto/openpgp (GO-2026-5932).

### staticcheck on master (before this branch)

All fixed; none suppressed except two ST1005 with `//lint:ignore` (the
"punctuation" is the literal text: `TS_AUTHKEY=...`, `svc:`).

- Real: `mgmt/service_v1.go` empty critical section (a `delete(m.depStatus…)`
  left behind when depStatus went away); `rootdocs.go` `// go:embed` looked
  like a directive; `hostd/d1_test.go` dead assignment.
- Deprecated tailscale API: `tailcfg.NodeAttrFunnel` / `NodeAttrServiceHost`
  → `tailcfg/nodecap` (present in v1.104.x too); dropped the
  `PeerStatus.Capabilities` fallback (every entry is also a CapMap key);
  `http.Transport.Dial` → `DialContext` in a test.
- Dead code (unused on both linux and darwin): `cmd/tw` `compact`,
  `waitHostLive`; `e2e/cliflow` `must`; `hostd` `writeErr`,
  `object.namespace`, `workload.classTags`; `mgmt` `ContainerReservation.key`;
  `mgmt/ui` `compact`, `orDash`; `netd/sbxnet` `oneConn.once`; guest
  `restarts`, `bpfFix.ja`; a test field.
- Style: `time.Until`, `http.Status*` constants, a redundant nil check.
- `cmd/tw-vz`: `run` returns only on error, so the `if err != nil` was
  always true; made that explicit.

## Tests

- `make lint`: pass (vulncheck step reports GO-2026-6859 and is ignored, as intended).
- `make test`: every Go package ok except one flaky `hostd` failure,
  `TestLockTemplate/KillHolderReleases` (lock contender saw the holder
  still renewing; same test family as flaky #136, untouched code). Rerun
  `go test -race -count=3 -run TestLockTemplate ./hostd/`: ok.
  `make test-js`: ok (0 fail).
- `make staticcheck`: clean (~1 min cold).
- `make vulncheck`: 1 finding, GO-2026-6859 (#146), as expected.
- `bin/lint/staticcheck ./cmd/tw-vz/` on darwin/arm64: clean.
- Scratch `-modfile` build+vet against tailscale.com v1.104.5: ok;
  v1.104.7: `go get` refuses (needs go1.27.4).
- Not run: `make e2e-placement` / `e2e-real` (tailscale.com not bumped);
  `make test-pg` (no store change).

- After merging master (with #117), staticcheck flagged one new SA1019 in
  `netd/internet.go` (`Status.MagicDNSSuffix`, a deliberate fallback after
  `CurrentTailnet`); suppressed with `//lint:ignore` and a reason. Expect
  this on every branch that merges after this lands: run `make lint`.
- After each merge of origin/master: `make lint test` pass (3 merges).

## Gaps / next

- #146: go1.27.4, then drop the `-` before `$(MAKE) vulncheck` in `make lint`.
- tailscale.com v1.106.0 when it's tagged (Dependabot's "tailscale" group
  will open it).
- staticcheck covers default build tags only; the e2e-tagged packages are
  still `go vet` only.
- `status/ci-pr-gates.md` still says Go/npm Dependabot is "tracked in a
  separate issue"; that's #112, now done (not edited: another workstream's
  file).
