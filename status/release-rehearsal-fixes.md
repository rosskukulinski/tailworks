Last update: 2026-11-23 13:30 PT. Fixes from the first release.yml rehearsal.

# Release rehearsal fixes

The first rehearsal (branch release-all-runtimes, run 45260846780) passed
every hermetic suite, gates, headscale, test-pg and the guest builds. It
failed on:

- `test-images`: TestCLIImages skipped ("needs workerd and node"), since the
  job didn't run `make deps`. Fixed: the job runs it.
- `microvm-amd64`, `mac-vz`: e2e-vm/e2e-vz passed but scripts/ci-suite.sh
  failed them for skips that are expected there: TestBootBench (opt-in
  benchmark), TestContainerSmoke / TestVZContainer* (run in the container
  step that follows, with images), TestE2EVMSnapshot on vz (Firecracker
  only). Fixed: per-step TW_CI_ALLOW_SKIP, with the reason beside it.
- `mac-firecracker`: the Lima dev VM had go1.27.3, go.mod wants 1.27.4
  (GOTOOLCHAIN=local). Fixed: scripts/ci-lima-make.sh installs go.mod's Go
  in the VM from go.dev, SHA-256 checked. Tried on the runner's VM: ok.
- `build`, `tailnet`: expected on a branch (the release environment deploys
  only from master and v* tags).

Seen, not failing: on vz the "no keys in guest memory" scan couldn't save
VM state ("permission denied"), so guest memory wasn't scanned under the
runner; files and disks were.

## Rehearsal 2 → real-tailnet suites (2026-11-23)

Run locally on the release runner's machine (same credentials) before the
next rehearsal, one suite at a time:

- e2e-oauth: pass after #265 (tag:tw-wl-mcp2 owned).
- e2e-mm-svc, e2e-coldid-real: pass.
- e2e-svc (TestE2EService, TestE2EReplicas): two stale assumptions, both
  from changes the suite hadn't been run against: `tw up` is single-process
  by default (the suite now passes `--separate-host`, as cliflow does), and
  minting is OAuth-only, so the run's OAuth client needs the `services`
  scope for a workload with a Service (cliflow.RealOAuthClientScopes). Now
  pass.
- e2e-svc-caps: tailnet 2's policy had lost the H22 entries. The test now
  adds them itself (internal/tssvc/real_caps_policy_test.go: additive,
  validated, If-Match, previous policy saved next to the key file), with
  the user's OK. Now pass.
- e2e-public: the first run got its first public answer in 48 s, then a
  route fetch failed on a transport error (IPv6 no route; next run, DNS no
  such host). fetch now retries dial/DNS errors 3× (any response is
  returned as is). A third run never got a Funnel certificate in 12 min
  (cert prefetch timed out twice; CI saw an ACME DNS-01 NXDOMAIN for a mgmt
  name): certificate issuance, not tailworks. Open: every run mints certs
  for fresh *.tail7906d5.ts.net names, and Let's Encrypt limits certs per
  registered domain per week.

## Rehearsal 3 (run 45283961679)

Hosted suites, both Firecracker jobs, gates, build: pass. Six of seven
templates pass on all three tarballs; e2e-public got its Funnel certificate
this time.

- **Product bug, fixed:** the node-workflow template couldn't build from an
  installed release (`Cannot find package 'esbuild'`): build.mjs and
  build-node.mjs imported esbuild statically from create-tailworks, and a
  release vendors only pure-JS modules (esbuild is native; tw has its own
  built in). build-node.mjs now loads esbuild from the app (as it loads
  @workflow/builders), build.mjs loads it lazily, and the template depends
  on esbuild 0.28.2 itself. Reproduced with create-tailworks copied away
  from any esbuild: failed before, serves after.
- e2e-public: a route fetch got NXDOMAIN from public DNS for the new Funnel
  name (cached negative answer). fetch retries dial/DNS errors for up to a
  minute (was 3 × 2 s).
- **mac-vz TestContainerSmoke: canary hits 2** while every guest probe was
  refused. Not reproduced: 13/13 passes locally with the same images and
  guest. The canary listens on all of the runner machine's addresses, so
  the hits may be another process on that machine, but that is not shown:
  the test now records each hit's source and local address, so a recurrence
  says where it came from. Not loosened.

## Rehearsal 4 (run 45291083895)

Everything passed except two jobs: gates, every hermetic suite, the signed
build, all three tarballs (node-workflow fix confirmed), both Firecracker
tiers, and e2e-vz with the six new microVM feature tests.

- mac-vz TestContainerSmoke: canary clean this time. It failed one step
  later: the limits probe after the pids storm hit pids.max ("failed to
  create new OS thread … errno=11") because the storm was still draining
  (slower under Rosetta) after the fixed 4 s sleep. The probe now retries
  only on EAGAIN, up to 30 s; the assertion is unchanged. 5/5 locally.
- e2e-public: /slow (a streaming request, not through fetch) got NXDOMAIN
  for the Funnel name 39 s after it first answered. publicClient now keeps
  each name's last good public DNS answer and dials it when a later lookup
  fails (still over the internet to the Funnel edge; TLS still checks the
  name).
- Self-hosted jobs: setup-go `cache: false` (its restore was writing into
  the runner user's own ~/go/pkg/mod; harmless, read-only, but not CI's).

## Rehearsal 5 (run 45300078043)

26 of 27 jobs passed, mac-vz included (pids drain fix; canary clean). The
hosted tailnet job got to e2e-public's last check: `audit_public_requests`
wanted ≥12 public_request records and saw 8. Stale test: since H03 (#68,
2026-11-15) netd rolls not_public/bad_request refusals into a per-minute
`public_rejected` summary; the test (2026-11-13) counted its 4 refused route
checks as records (12 − 4 = 8). The check now wants ≥8 public_request
records and a public_rejected summary with notPublic ≥2 and badRequest ≥1,
waiting up to 90 s for the window.

## Rehearsal 6 (run 45307109374) and the faster layout

27 of 28 jobs passed, including, for the first time, both real-tailnet
jobs: every distro row from the tarballs, the microVM tier from the arm64
tarball with host reboots, e2e-mac, e2e-multi, and all hosted suites.

- microvm-amd64 TestPoolNoCarryOver: the pooled VM exited during its
  memory dump ("helper connection closed"), so the RAM scan never ran (not
  a leak). Not reproduced: 20/20 on Firecracker in the runner machine's
  Lima VM. Ruled out: the jail's memory.max (with hugepages it is only the
  256 MiB overhead, and the dump's 512 MiB page cache is charged to it), as
  5/5 still pass at 32 MiB. The test now reports the VM's exit reason and
  console tail. The same dump backs `tw host vmcheck --keyscan`.

Faster release.yml (the user's ask): tailnet no longer waits for gates
(#276); `only=` reruns selected jobs (#276); gates is a 5-way matrix;
mac-tailnet split into mac-linux (arm64 rows, 3 at a time), mac-linuxvm,
mac-cli (still after tailnet: writes policy); the ubuntu24-amd64 row runs
natively on a hosted runner (linux-amd64: Lima + qemu on KVM); the Linux
jobs run alongside tailnet on tailnet 1 (the harness was built for parallel
rows; they only read policy). Two more runner instances on the Mac (3).
