Last update: 2026-11-22 09:40 PT. tw up / tw down / restart regressions (#245) found and fixed; budgets pinned.

# perf-245-up-down (issue #245)

## What was slow, and why

Reproduced on macOS and on Lima `twdl-ubuntu24-arm64` (Ubuntu 24.04 arm64)
against testcontrol with a fake Tailscale API, which is hermetic. In the VM,
`tw` ran as a systemd user service, as `e2e-linux` runs it. The numbers
matched the matrix: `tw up --service` took 3.5–6.5 s and
`tw down --service` 5.1 s every time.

1. **`tw down` / service stop: +5 s.** mgmt's `Close` spent 5.0 s in
   `tsnet.Server.Close`, not in `http.Server.Shutdown`; the listeners closed
   in 0 ms. tsnet's `Close` gives its LocalAPI server a 5 s shutdown grace.
   mgmt's HTTPS certificate prefetch (`prefetchCert`, added with the
   single-process layout on 2026-11-14) runs `lc.CertPair` for up to
   3 min in the background. Right after a start it was always in flight,
   so every stop waited out the full grace. That explains "mgmt exits 5 s
   after hostd". Public workload nodes (`netd.PrefetchCert`) had the same
   hold on their own node `Close`.
2. **`tw up`: ~3 s per tsnet start on Linux.** `tw up`'s in-process join of
   mgmt's node (`joinMgmtNode`) waited for state `Running`. Running needs a
   live DERP connection, so it waited for the first netcheck too. In the VM
   that was 3.0 s per run, the netcheck STUN timeout. mgmt's own start
   (`tsnetUp`, then `ts.ListenTLS`, which calls `Up` again) waited for the
   same thing. On a node with no DERP home yet, that added another ~3 s.
   This is the 3.7 s mgmt ready plus 2.3 s host ready in the matrix.
3. **Reboot to healthy.** Health (`tw status`) needs mgmt's API over its
   own node. Before, mgmt served only once its node was Running, which
   means DERP reachable. During a boot the network comes up late, and
   netcheck and DERP back off. I didn't do a real reboot; see the gaps
   section. Simulated in the VM by dropping DERP and STUN with iptables
   for 10 s around `systemctl --user restart tw`: healthy after 9.6 s
   before, 0.31 s after. With control itself unreachable as well, both
   builds wait for control: 13.3 s before, 11.5 s after, with the block
   lifted at 10 s.
4. **Warm-key revoke on close (#231).** It was sequential: one API round
   trip per key. This isn't visible against the fake API, but with real
   latency, tens of keys take seconds. Revocation now runs 8 at a time.

The netd watcher from #232 and the sandbox were not involved. Host close
took < 1 ms, and `hostdrun.New` with Landlock took ~5 ms.

## What changed

- `internal/mgmtd/mgmtd.go`:
  - `Runtime.bg` is cancelled first in `Close`. The certificate prefetch
    runs under it, so it can't hold the node's `Close`.
  - `tsnetUp(…, earlyOK)` returns once the node is logged in with a
    netmap and an address (`LoggedIn`: state Starting or Running). Then
    `ts.Up` runs in the background under `bg` for its one-time
    serve-config reset. A Tailscale Service replica (`TSNetService`) keeps
    the old wait for Running, because that reset would clear its
    advertisement.
  - The :443 listener is `ts.Listen` plus `tls.NewListener` with the
    LocalAPI's `GetCertificate`, instead of `ts.ListenTLS`, which waits for
    Running.
  - `Close` logs `stopped` with per-phase ms, and the `tailnet node` line
    carries `state` and `ms`, so a slow stop or start shows where the time
    went.
- `cmd/tw/login.go`: `joinMgmtNode` stops waiting once `mgmtd.LoggedIn`
  holds, because name, tailnet, tags, IP, cert domains and caps all come
  from the netmap.
- `netd/certprefetch.go`, `netd/public_serve.go`: `PrefetchCert` takes a
  ctx. A public node's prefetch is cancelled in `teardown`, which is one
  added call in `netd/registry.go`, before `srv.Close`.
- `mgmt/warmrevoke.go` (new): `revokeWarmAll`, 8 at a time.
  `drainWarm` and the shutdown retry of failed revocations use it.

## Before and after

Hermetic numbers (testcontrol and the fake API; real control adds its RTTs
to each tsnet start):

| | before (master `4fe16ca`) | after |
|---|---|---|
| Linux VM, `tw up --service` (3 runs) | 3.56 / 3.47 / 3.49 s (6.4 s seen) | 0.51 / 0.46 / 0.48 s |
| Linux VM, `tw down --service` | 5.12 / 5.15 / 5.13 s | 0.25 / 0.18 / 0.26 s |
| Linux VM, `systemctl --user restart tw` returns | 5.03 s | 0.20 s |
| Linux VM, restart → healthy with DERP/STUN unreachable | 9.6 s | 0.31 s |
| macOS, `tw up` (re-up on saved state) | 1.1 s | 0.5 s |
| macOS, `tw down` | 5.03 s | 0.07–0.12 s |

## Tests that pin it

- `internal/mgmtd` `TestStartCloseBudget` (runs in `make test`): mgmt on
  testcontrol, first start, then a restart with no DERP or STUN. The
  budgets are Start ≤ 10 s / ≤ 2 s and Close ≤ 2.5 s. Fails before the
  fix: the restart took 3.1 s against its 2 s budget on macOS. On Linux
  the old start was fast in-process (36 ms), so the e2e test below is the
  Linux guard for `tw up`.
- `mgmt` `TestWarmKeysRevokedOnCloseInParallel`: 16 keys at 200 ms per
  DELETE, budget 1.6 s. One at a time takes 3.24 s, which fails; now it
  takes 0.43 s.
- `e2e/twupgrade` `TestUpDownBudget` (runs in `make e2e-tw-upgrade`): the
  real `tw` binary does up, down, up, down, up, down --purge, with up
  ≤ 4 s and down ≤ 2.5 s. Fails on master: every down took 5.0 s.

## Tests run

- `make lint`: PASS (after merging origin/master `4fe16ca`).
- `make changes-check`: ok (no upgrade-relevant paths). Added
  `changes/perf-245-up-down.md` anyway, since the change is user-visible.
- `make test` (mbp runner, slot1): PASS.
- `make e2e-tw-upgrade`: PASS (`TestTwUpgradeSingleMachine` 68 s, with
  deploy, KV and D1 across restarts and rollbacks; `TestUpDownBudget`
  3.8 s).
- `go test -race -run 'TestStartCloseBudget' ./internal/mgmtd` and
  `-run 'TestWarmKeysRevokedOnClose' ./mgmt`: PASS. The mgmtd one also
  passed 3× in the Linux VM.
- VM measurements: a throwaway variant of `TestUpDownBudget` (prebuilt
  binaries, `--service`, iptables-blocked restart), not committed.
  `twdl-ubuntu24-arm64` was started for this and is stopped again; `/tmp/ud`
  was removed in it.

## Not run / gaps

- `make e2e-linux` (real tailnet 1, with a reboot) is not run: it needs
  the tailnet credentials and writes to a real tailnet. It is the one
  measurement that would confirm the reboot → healthy number on
  production control. Expected: the 5 s stop and the netcheck/DERP waits
  are gone. What remains is control's own reachability after boot.
- With control unreachable at start, health still waits for control.
  tsnet's netmap disk cache didn't bring the node to Starting before
  control answered in this test.
- `e2e-public` (Funnel and public nodes; credentials) is not run. The netd
  change is a cancel-before-close of the certificate prefetch only.
