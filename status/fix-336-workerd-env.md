Last update: 2026-11-24 10:30 PT. workerd gets a minimal environment on every process-tier branch (#336, ledger H-workerd-env); regression tests fail on master and pass on the branch on macOS (none, Seatbelt) and Linux (none, Landlock ABI 4).

# fix-336-workerd-env: workerd no longer inherits tw-hostd's environment

Issue [#336](https://github.com/rosskukulinski/tailworks/issues/336) (Workers
threat-model review, Sev Low, hostile tenant code after a V8 escape): every
branch of `inst.sandboxed()` returned an `exec.Cmd` with no `Env`, so the
workerd process (Landlock through the procsb launcher, `sandbox-exec`, or no
sandbox) had tw-hostd's whole environment: under a foreground `tw up` the
operator's shell, under `--service` the service manager's. The Node tier
already built its own list (`spawnNode`, docs/node-tier.md).

## What changed

- `hostd/workerdenv.go` (new): `hostd.WorkerdEnv()` builds the list from
  nothing. Only `TZ`, and only when hostd has one. `workerdCommand` sets it
  on the `exec.Cmd` that `sandboxed()` returns, so all three branches get
  it with one call (`hostd/seatbelt.go`). The Node tier is untouched: it
  appends its own list to the command `sandboxed()` returns, as before.
- `internal/procsb/procsb.go`: `procsb.Command` starts the launcher with an
  empty environment (`Env = []string{}`, not nil). The launcher forwards the
  environment it was started with and adds nothing, so a child has exactly
  what the caller put in `Env`. The launcher can't hold an allow list of its
  own: the Node tier's list (`TW_CALLER_KEY`, the proxy variables,
  `node.env`) is the caller's to decide, and there is no other channel to
  the launcher that doesn't put secrets in argv or on disk. The comment at
  the `syscall.Exec` says so (`internal/procsb/launch_linux.go`).
- `cmd/tw/doctor_sandbox.go`: both of `tw doctor`'s workerd probes
  (`sandbox-exec … workerd --version`, launcher `… workerd --version`) run
  with `hostd.WorkerdEnv()` too; before, the Linux one rebuilt the
  `exec.Cmd` and so inherited tw's environment.
- microVM guest (`guest/tw-guestd/main_linux.go`): checked, no change. All
  three places that start workerd (warm-up, serve, the debug runner) set
  `cmd.Env = {"HOME=/tmp", "PATH=/usr/bin:/bin", "TZ=UTC"}` explicitly, so
  the guest's workerd never had tw-guestd's environment.
- docs/security-model.md: an `Environment` row in "What the process-tier
  sandbox confines" (process tier and the microVM guest's list).
- security/LEDGER.md: hypotheses row `H-workerd-env` (Low, hostile tenant
  code, **fixed**, test names) and a run-log line, both appended.
- changes/fix-336-workerd-env.md: `kind: security` (`make changes-check`
  didn't require one; the behaviour change for Workers is worth a note).

## Measurement: what workerd reads

`strings -n 2 node_modules/workerd/bin/workerd` (macOS arm64 build of the
pinned workerd), filtered to names that look like environment variables, and
`nm -u` (the binary imports `getenv` and `_NSGetEnviron`). Names the binary
and the libraries it links (ICU, V8, KJ, OpenSSL) know: `TZ`, `LANG`,
`LC_ALL`, `LC_MESSAGES`, `TMPDIR`, `TMP`, `NO_COLOR`, `USER`, `PWD`,
`ICU_DATA`, `ICU_TIMEZONE_FILES_DIR`, `ICU_ENABLE_TENTATIVE_ERA`,
`MALLOC_FAILURE`, `WORKERD_ALL_AUTOGATES`, `WORKERD_GC_STRESS`,
`WORKERD_SECRET_` (prefix for `fromEnvironment` bindings; tailworks's
generated config uses none). Not in the list: `HOME`, `PATH`.

Decision per name:

- `TZ`: passed through when hostd has it. Without it workerd reads
  `/etc/localtime` (ledger H26 measured that read), so a host with no `TZ`
  behaves exactly as before.
- `LANG`/`LC_*`: not passed. They set ICU's default locale; when unset ICU
  falls back to `en_US_POSIX`, which V8 maps to `en-US`
  (`Intl::DefaultLocale`), so every workload now has the same default
  `Intl` locale, as on Cloudflare. Before, it followed the operator's shell.
- `TMPDIR`/`TMP`: not passed. Both sandboxes already refused `/tmp` and the
  suites passed, so workerd doesn't need a temp dir at run time.
- `NO_COLOR`, `USER`, `PWD`, `WORKERD_*`, `ICU_*`: not passed; nothing in
  tailworks sets them for workerd.
- `HOME`, `PATH`: not read by workerd. hostd starts it by path
  (`FindWorkerd` returns a path with a slash; the launcher's `LookPath` of
  such a path doesn't consult `PATH`), and workerd execs nothing.

Then confirmed by running the suites with the rest absent (below).

## Tests

Regression tests (hard rule 8: fail before, pass after, on the production
path: hostd's own `workerdCommand` → `cmd.Start`, read from the live
process):

- `hostd.TestWorkerdEnvNotInherited/{none,seatbelt}` (macOS) and
  `/{none,landlock}` (Linux): `t.Setenv` a canary, start hostd with that
  sandbox, deploy the `probe` fixture, read the workerd pid's environment
  (`/proc/<pid>/environ`; `ps -E -o command= -p` on macOS). The canary and
  every other variable the test process has must be absent; `WorkerdEnv`'s
  variables present; on Linux the count must match exactly. Only names are
  logged, never values (a developer shell's environment holds secrets).
- `procsb.TestLauncherEnvNotInherited` (Linux): `/bin/cat
  /proc/self/environ` through `procsb.Command` sees nothing by default and
  exactly the caller's `Env` list when one is set.

Before (test files on the unfixed code; `WorkerdEnv` present but not wired):

```
macOS (arm64, sandbox-exec):
=== RUN   TestWorkerdEnvNotInherited/none
    workerdenv_test.go:59: hostd's TW_TEST_CANARY_336 reached the workerd process
    workerdenv_test.go:79: hostd's HOME reached the workerd process
    workerdenv_test.go:79: hostd's SSH_AUTH_SOCK reached the workerd process
    … (every variable of the shell: ~80 names, several of them API keys)
=== RUN   TestWorkerdEnvNotInherited/seatbelt
    workerdenv_test.go:59: hostd's TW_TEST_CANARY_336 reached the workerd process
    workerdenv_test.go:79: hostd's HOME reached the workerd process
    workerdenv_test.go:79: hostd's SSH_AUTH_SOCK reached the workerd process
    …
--- FAIL: TestWorkerdEnvNotInherited (1.96s)

Linux (twd-amd64, Ubuntu 24.04, kernel 6.8, Landlock ABI 4; TW_TEST_SANDBOX=auto):
=== RUN   TestWorkerdEnvNotInherited/none
    workerdenv_test.go:59: hostd's TW_TEST_CANARY_336 reached the workerd process
    workerdenv_test.go:79: hostd's HOME reached the workerd process
    workerdenv_test.go:79: hostd's PATH reached the workerd process
    workerdenv_test.go:79: hostd's SSH_CONNECTION reached the workerd process
    … (19 variables)
=== RUN   TestWorkerdEnvNotInherited/landlock
    workerdenv_test.go:59: hostd's TW_TEST_CANARY_336 reached the workerd process
    … (the same 19)
--- FAIL: TestWorkerdEnvNotInherited
```

After:

```
macOS:
    workerdenv_test.go:63: workerd pid 33230 (none) has 0 environment variables: []
    workerdenv_test.go:63: workerd pid 33494 (seatbelt) has 0 environment variables: []
--- PASS: TestWorkerdEnvNotInherited (2.39s)
    --- PASS: TestWorkerdEnvNotInherited/none (1.04s)
    --- PASS: TestWorkerdEnvNotInherited/seatbelt (1.35s)

Linux:
--- PASS: TestLauncherEnvNotInherited (0.02s)
ok  	github.com/rosskukulinski/tailworks/internal/procsb	0.024s
    workerdenv_test.go:63: workerd pid 73882 (none) has 0 environment variables: []
    workerdenv_test.go:63: workerd pid 73901 (landlock) has 0 environment variables: []
--- PASS: TestWorkerdEnvNotInherited (2.40s)
    --- PASS: TestWorkerdEnvNotInherited/none (1.29s)
    --- PASS: TestWorkerdEnvNotInherited/landlock (1.11s)
ok  	github.com/rosskukulinski/tailworks/hostd	2.434s
```

(0 variables because neither test host sets `TZ`.)

Suites, with the fix:

- `make lint` (macOS): PASS (gofmt, vet, staticcheck, api-check,
  compat-check, changes-check, check-secrets).
- `make test-node` (macOS): PASS (`e2e/node` 13.8 s).
- `TW_TEST_SANDBOX=auto go test ./hostd/... -count=1` (macOS, Seatbelt):
  first run FAIL on one test, `TestWorkerdWrapperKilled`, because of this
  change: its wrapper script read the real workerd path from
  `$TW_TEST_REAL_WORKERD`, which a workerd no longer inherits. The test now
  bakes the path into the script (second commit). Re-run of that test and
  the env test: PASS; every other package in the run was `ok`.
- `sh ~/.claude/scratch/mbp-test.sh test` (`make test` on mbp): PASS, exit
  0, 58 Go packages `ok`, no FAIL (synced after the wrapper-test fix).
- Linux (twd-amd64, 2 vCPU, 5 GB, Ubuntu 24.04, kernel 6.8, Landlock ABI 4):
  - `TW_TEST_SANDBOX=auto go test ./internal/procsb/... -count=1`: PASS.
  - `TW_TEST_SANDBOX=auto go test ./hostd -count=1 -timeout 40m`: the
    package aborts at `TestRealMgmtStores/D1/Delete` (panic in the test:
    the worker's `/session` answer after the delete has no `steps`; the
    test asserts on it unguarded). **Pre-existing and not this branch's:**
    the same subtest fails the same way on an unfixed `origin/master`
    checkout on the same VM (`~/tw-336-master`, `go test ./hostd -run
    TestRealMgmtStores/D1`), and passes on macOS and on mbp. Full package
    with that subtest skipped (`-skip TestRealMgmtStores/D1/Delete`, 326 s):
    two more failures, both also **pre-existing on the same unfixed master
    checkout on this VM**, verified by running each there: `TestD1/Delete`
    (the same nil-`steps` panic, the non-realmgmt variant of the same
    test) and `TestSharedHostTailscaledUnixListen` (Landlock-only, so it
    ran nowhere else: "deploy nodeprobe failed: node app did not answer /"
    after 30 s, identical on master with the Node deps installed). Every
    other test in the package passed under Landlock, including the
    multi-tenant Landlock tests, the Node-tier tests and the new
    `TestWorkerdEnvNotInherited`. (The first attempt with the default 10 m
    timeout timed out: the VM is ~4x slower than the Macs.)
  - `TW_TEST_SANDBOX=auto make test-node`: one failure, `TestNodeWorkflow`
    ("want 3 marks from 3 processes"), while the hostd suite was running
    on the same 2 vCPUs: the "slept" mark came 2.25 s after "triaged" in
    the same pid, so the test's kill arrived after the 2 s sleep had run.
    Timing, not environment (the Node tier's list is unchanged). Alone:
    PASS (78.7 s).

Skipped: `e2e-vm`/`e2e-vz` (the microVM guest is unchanged), the
credentialed suites.

Needs a decision (not done here): file `TestRealMgmtStores/D1/Delete`,
`TestD1/Delete` and `TestSharedHostTailscaledUnixListen` on Linux as their
own issue(s); all three fail on master on the amd64 test VM (Ubuntu 24.04,
kernel 6.8, Landlock ABI 4), none on macOS.

## Notes and gaps

- The one visible change for Workers: the default `Intl` locale is `en-US`
  on every host (before: whatever the operator's `LANG` said). Time zone
  unchanged.
- Residual, by design: the Landlock launcher forwards what its caller set.
  The guarantee is `procsb.Command`'s empty default plus the caller's list;
  both are pinned by `procsb.TestLauncherEnvNotInherited`.
- The process-tier workerd keeps the host's time zone while the microVM
  guest's runs in UTC. Pre-existing; not changed here.
