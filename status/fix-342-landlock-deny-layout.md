Last update: 2026-11-24 PT. Fix for issue #342: the Linux process-tier sandbox could not keep tenant code out of tailworks state or credentials placed under a sandbox-readable OS tree; tailworks now refuses the layout, and `tw doctor` checks the real paths.

# fix-342-landlock-deny-layout

Issue: [#342](https://github.com/rosskukulinski/tailworks/issues/342). Ledger row `H-342-landlock-deny` in `security/LEDGER.md`. Options 1 (refuse the layout) and 3 from the issue (doctor checks the real thing) are implemented; option 2 is a follow-up (below).

## What's built

- `internal/procsb/layout.go` (every OS): `LayoutPath`, `LayoutError`, `CheckLayout(paths, trees)`: a path that resolves (symlinks followed, dangling ones by hand, a not-yet-created path through its longest existing prefix) to one of `trees` or below one is refused with the typed code `sandbox_layout_unsafe`. `layout_linux.go`: `ReadableTrees()` = `systemRO` (`/usr`, `/lib`, `/lib64`, `/lib32`, `/bin`, `/sbin`, `/etc`, `/opt`, `/nix/store`, `/run/current-system`) and `Profile.checkLayout`, which `Profile.Apply` runs in the launcher before any Landlock rule: every `Deny` entry against `systemRO` and the profile's `ReadOnly` trees, so no workload starts under a profile whose deny list Landlock can't honour. `layout_other.go`: `ReadableTrees()` is nil off Linux (Seatbelt's deny rules win inside an allowed dir).
- `hostd/sandboxlayout.go`: `checkSandboxLayout` (data dir, run dir, every `SandboxDeny` path, the home dir) called once in `hostd.New` right after `ResolveSandbox` and before anything is created; `checkTenantLayout` (the tenant's `apiKeyFile` / `oauthClientFile`) called once in `ensureTenant`. Both only when the sandbox is Landlock.
- `cmd/tw/sandbox_layout.go`: `sandboxDenyList` (tw up's deny list, moved from `up.go`), `sandboxLayoutPaths` (state dir, run dir, every tenant's credential file from tw.json, home dir), `checkSandboxLayout` → `cliErr` with a fix per role, `checkUpLayout` (one call in `tw up` after the credential file is validated), one call each in `tw tenant add` and `tw tenant set --oauth-client-file`, and `doctorLandlockLayout`: `tw doctor`'s `sandbox` check now probes every path hostd would deny under the doctor profile (`ProbeList` for a dir, `ProbeRead` for a file) and fails naming the path and the tree it is under, plus a location check for paths not created yet. The old "Landlock did not enforce … (the home dir was listable)" message for a home under `/opt` is gone: the failure names the home dir and `/opt`.
- Docs: `docs/errors.md#sandbox_layout_unsafe` (When / Fix), `docs/security-model.md` (Linux Reads column: the deny list is not a Landlock rule, where state and credentials must not live, what else under `/etc` and `/opt` is readable; Secrets and state), `docs/INSTALL.md` (state dir and credential file placement, the error in the troubleshooting list), `security/THREAT-MODEL.md` §3 (Linux row and the closing paragraph), `security/LEDGER.md` (hypothesis row, run log), `changes/fix-342-landlock-deny-layout.md` (kind: security).

## Tests

Regression test `attacks.TestRT_H342_LandlockDenyLayout` (Linux only, skips without Landlock). Part 1 drives the real launcher with a temp dir standing in for `/opt` (in the profile's `ReadOnly`, the same `landlock.RODirs` rule `systemRO` gets), a `custody.key` and an `oauth.env` below it and both in `Deny`; `ProbeRead` on each must be refused. Part 2: `hostd.New` with `Sandbox: landlock` must refuse a data dir under `/opt`, a symlink into `/opt`, a run dir under `/usr/local` and a `-sandbox-deny` path under `/etc` with `*procsb.LayoutError`, before creating anything, and accept the same config outside the trees. Unit test `procsb.TestCheckLayout` runs on every OS.

Before (origin/master `1fc94cc3` plus the new test and the `layout*.go` types only, so it compiles), on `twd-amd64` (Ubuntu 24.04, kernel 6.8.0-149, Landlock ABI 4, amd64, Go 1.27.4):

```
$ go test ./security/attacks/ -run TestRT_H342 -v
=== RUN   TestRT_H342_LandlockDenyLayout
    rt_sandbox_linux_test.go:263: H-342 BROKEN: the custody key (/tmp/…/opt/tailworks/.tailworks/mgmt/custody/custody.key) under a sandbox-readable tree was READ by a sandboxed workload despite being in the deny list
    rt_sandbox_linux_test.go:263: H-342 BROKEN: a tenant's OAuth client file (/tmp/…/opt/tailworks/etc/oauth.env) under a sandbox-readable tree was READ by a sandboxed workload despite being in the deny list
    rt_sandbox_linux_test.go:292: H-342 BROKEN: hostd.New with run dir under /usr/local: got mkdir /usr/local/tailworks: permission denied, want sandbox_layout_unsafe
    rt_sandbox_linux_test.go:292: H-342 BROKEN: hostd.New with state dir symlinked into /opt: got mkdir /tmp/…/var-lib-tailworks: file exists, want sandbox_layout_unsafe
    rt_sandbox_linux_test.go:292: H-342 BROKEN: hostd.New with state dir under /opt: got mkdir /opt/tw-test-h342-75010: permission denied, want sandbox_layout_unsafe
    rt_sandbox_linux_test.go:292: H-342 BROKEN: hostd.New with credential file under /etc: got <nil>, want sandbox_layout_unsafe
--- FAIL: TestRT_H342_LandlockDenyLayout (0.04s)
FAIL
```

After (this branch, same machine):

```
$ go test ./internal/procsb/... ./security/attacks/... -run 'RT_|Sandbox|Layout' -v
--- PASS: TestCheckLayout (8 subtests)
--- PASS: TestSandboxProbes (1.53s)
--- SKIP: TestWorkerdUnderSandbox (workerd not installed at that point; passes after make deps, see below)
--- PASS: TestNodeUnderSandbox (0.84s)
ok  	github.com/rosskukulinski/tailworks/internal/procsb	2.438s
--- PASS: TestRT08_LinuxSandboxEscapeBattery (0.53s)
    rt_sandbox_linux_test.go:269: the custody key              -> refused: tw sandbox: sandbox_layout_unsafe: the denied path /tmp/…/opt/tailworks/.tailworks is under /tmp/…/opt, which every Landlock-sandboxed workload may read (Landlock has no deny rules); …
    rt_sandbox_linux_test.go:269: a tenant's OAuth client file -> refused: tw sandbox: sandbox_layout_unsafe: …
    rt_sandbox_linux_test.go:295: state dir symlinked into /opt  -> hostd refused: sandbox_layout_unsafe: the hostd data dir /tmp/…/var-lib-tailworks (/opt/tw-test-h342-76752/state) is under /opt, …
    rt_sandbox_linux_test.go:295: state dir under /opt           -> hostd refused: sandbox_layout_unsafe: the hostd data dir /opt/tw-test-h342-76752/state is under /opt, …
    rt_sandbox_linux_test.go:295: credential file under /etc     -> hostd refused: sandbox_layout_unsafe: the sandbox-deny path /etc/tailworks/oauth.env is under /etc, …
    rt_sandbox_linux_test.go:295: run dir under /usr/local       -> hostd refused: sandbox_layout_unsafe: the hostd run dir /usr/local/tailworks/run is under /usr, …
--- PASS: TestRT_H342_LandlockDenyLayout (0.07s)
ok  	github.com/rosskukulinski/tailworks/security/attacks	0.642s
```

Other runs:

- twd-amd64, after `make deps` (workerd and node present): `TW_TEST_SANDBOX=auto go test ./hostd/ -run 'MultiTenant|Sandbox|Egress|Tenant' -count=1` → `ok … hostd 58.750s` (the multi-tenant Landlock probes, egress enforcement and tenant tests under the real sandbox, with hostd's new start-time check in the path). `go vet ./cmd/tw/ ./hostd/ ./internal/procsb/ ./security/attacks/` on Linux: ok.
- twd-amd64, `tw doctor` built from this branch, `--state-dir` a symlink into `/opt` (no `~/.tailworks` touched): `✗ sandbox  the state dir /tmp/h342/var-lib-tw (/opt/tw-h342-nonexistent/state) is under /opt, which every sandboxed workload may read once it exists on Linux 6.8.0-149-generic: Linux's Landlock sandbox has no deny rules: … fix: use a state dir outside /usr, /lib, …`. With a plain temp state dir: `✓ Isolation  sandbox (Landlock ABI 4 + seccomp) · host sockets`.
- macOS: `go test ./docs/ ./internal/procsb/` ok; `go test ./cmd/tw/ -run 'CLIReference|CLIJSON|Errors|Doctor'` ok (no command, flag or help text changed, so `docs/cli.md` is unchanged).
- macOS: `make lint` → exit 0 (api-check, compat-check, changes-check, vet of every build tag, check-secrets).
- `make test` offloaded to mbp (`sh ~/.claude/scratch/mbp-test.sh test`, on the branch before the `origin/master` merge): exit 0, every Go package `ok` (130), JS tests ok, no FAIL.
- After merging `origin/master` (one conflict, `security/LEDGER.md`: both branches appended a row; both kept, master's first): `make lint` exit 0; `go test ./cmd/tw/ ./internal/procsb/ ./security/attacks/ ./docs/` ok on macOS.

Not run: `e2e-real`, `e2e-public`, `e2e-compat` and the other credentialed or VM suites (nothing in their table rows changed; the sandbox change is covered by the Linux runs above).

## Decisions

- The home dir is part of the refusal (hostd, tw up, launcher), not only the state dir, run dir and credential files the issue names: hostd's deny list promises it, and a service account with its home under `/opt` and the default state dir fails on the state dir anyway. The fix text says how to move it (`usermod -d`, or `HOME`).
- The launcher check (`Profile.Apply`) is defence in depth behind hostd's: it covers a tenant whose credential file is moved under `/etc` after hostd learnt the tenant (hostd's `sandboxDeny` re-reads every tenant's paths from mgmt at each launch), at the cost of a workload failing to start with exit 126 and the typed message on stderr instead of a typed deployment error.
- No change to the `/etc` and `/opt` allow list in this PR.

## Follow-ups

- Option 2 from the issue: narrow the `/etc` and `/opt` trees in `systemRO` to what workerd and node actually open (CA certificates, `ld.so.cache`, `localtime` and the zoneinfo database, `nsswitch.conf`, `hosts`, `resolv.conf`, `passwd`/`group`), measured the way the macOS profile was for H26. Everything else under `/etc` and `/opt` the tailworks user can read is still readable by tenant code (documented in docs/security-model.md).
- Option 4: a separate uid for workloads, so file modes help too.
