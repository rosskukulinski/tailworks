Last update: 2026-11-22 PT. Node-tier listeners reachable only through ingress (ledger H-lan-bind fixed).

# Node app LAN exposure (H-lan-bind)

Follows `status/fix-175-seatbelt-reads.md` (found there). Branched from
fix-175-seatbelt-reads (PR #227) before it landed.

## Problem (measured)

- macOS 27.2 (Seatbelt): under `(local tcp[4] "localhost:<PORT>")` a
  sandboxed node's `listen(PORT)` (binds `::`, or `0.0.0.0` when IPv6 is
  refused) and a raw libuv `0.0.0.0:<PORT>` listener accepted connections
  on the Mac's LAN address. Seatbelt ignores the host in `local` filters
  and checks `listen()` as `network-inbound` by port only. It refuses
  literal addresses. Even with only an inbound rule for the inherited
  127.0.0.1 socket's port, binding `0.0.0.0:<PORT>` beside it succeeds
  (SO_REUSEADDR), so no Seatbelt rule can keep the port and refuse the
  wildcard.
- Linux 6.8, Landlock ABI 4 (an Ubuntu 24.04 VM): the app's `listen(PORT)` on `::`
  was reachable on the VM's address. An unbound `listen()` got
  `0.0.0.0:<ephemeral>` even with no bind port allowed: Landlock doesn't
  see autobind.

## Fix

- `hostd/nodelisten.go` (new): `setupNodeListen`, `nodeProc.dial`,
  `prepareSpawn`, `attach`, the forwarder and the preload
  (`tw-listen.cjs`).
  - **Landlock mode (`fd`):** hostd opens `127.0.0.1:0` (or the
    `listen: "unix"` socket) once per instance and passes it as fd 3 on
    every start. The profile has no TCP bind port. `procsb.Profile.NoListen`
    makes every `listen()` return 0 without effect (seccomp `RET_ERRNO`
    with errno 0), so node's re-listen of fd 3 works and nothing else ever
    listens. Dial refuses while the app is down, so ingress still answers
    503 instead of waiting in hostd's backlog.
  - **Seatbelt mode (`unix`, TCP-listen apps):** no TCP bind or inbound at
    all. The app listens on `<instance>/app-tcp.sock`; ingress and health
    checks dial it; hostd forwards `127.0.0.1:<PORT>` to it for self-calls
    (the Workflow local world's queue). `listen: "unix"` apps are unchanged.
  - **Preload:** `listen(PORT[, host])`, `listen({port: PORT})` and
    `listen(TW_SOCKET)` become `listen({fd: 3})` or `listen({path})`;
    `address()` reports `127.0.0.1:<PORT>`. Other listens are left alone
    (the sandbox refuses them).
- `hostd/node.go`, `hostd/seatbelt.go`: small hooks (one field, setup call,
  dial, preload, ExtraFiles, close; drop the node bind port; set NoListen).
- `internal/procsb`: `NoListen`, the seccomp rule, the `listen-tcp` and
  `accept-fd` probe ops.
- Not changed: workerd still binds its `tw dev` inspector port (it isn't
  native tenant code without a V8 escape). `-sandbox none` and
  `seatbelt-legacy` keep the old behaviour.

## Tests run

- `go test ./hostd/ -run TestRT_HLanBind -count=1` (macOS): FAIL before
  (`listen(PORT)` and a raw `<LAN>:<PORT>` listener reachable on
  the Mac's LAN address), PASS after. Every raw probe gets EPERM after.
- `procsb.TestNoListen` on an Ubuntu 24.04 VM (kernel 6.8, ABI 4;
  cross-compiled test binary): FAIL with the seccomp rule disabled
  (unbound, 0.0.0.0 and 127.0.0.1 listeners), PASS with it. Full procsb
  test binary: PASS.
- Linux, by hand on that VM (procsb launcher + node 22 + the preload, fd
  3): before, the app's `listen(PORT)` was reachable on the VM address;
  after, the app serves on 127.0.0.1 through fd 3, the LAN address is
  refused, a raw `0.0.0.0:<PORT>` bind gets EACCES, and an unbound listen
  returns 0 but doesn't listen.
- `TW_TEST_SANDBOX=auto go test ./hostd/... -count=1`: PASS.
- `make test-node` (Node tier + Workflow, Seatbelt): PASS.
- `make test-sdk`: PASS.
- Not run: the hostd node test on Linux. CI has no `e2e/node` deps, so it
  skips there; the Linux path is covered by `procsb.TestNoListen` and the
  manual run above.
