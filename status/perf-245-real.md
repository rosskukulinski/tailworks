Last update: 2026-11-22 15:10 PT. #245 on real control: restart and reboot fixed; what's left of `tw up` is control round trips.

# perf-245-real (issue #245, after #250)

Continues `status/perf-245-up-down.md`. I measured on test tailnet 1 with
`make e2e-linux LINUX_DISTROS=ubuntu24-arm64 TW_E2E_VM_PREFIX=twdl-`: a
fresh Lima VM per run, production control, and the reboot phase. That is 10
runs. The policy was not written, and every run left 0 devices and 0 valid
keys. `twdl-ubuntu24-arm64` is stopped.

## The matrix runs the non-default layout

`cliflow.Env` defaults to `--separate-host`: tw-mgmt and tw-hostd run as
two processes, and hostd has its own tailnet node. `tw up`'s default is
single-process. The 11-08 numbers came from the older unix-socket layout,
where mgmt had no node and hostd reached it over a socket, so they are not a
like-for-like target. New knob: `TW_E2E_LAYOUT=single` runs the matrix in
the default layout.

## Causes found

1. **Restart: +5 s on about half the runs (2 of 6).** I took a goroutine
   dump when tw-hostd's node took more than 1.5 s to close. It showed
   `tsnet.(*Server).close` → LocalAPI `http.Server.Shutdown`, and one
   server conn in `readRequest`, over a memnet pipe. The client side of
   that conn was a `persistConn` sitting unused in local.Client's
   transport pool. `Shutdown` counts a conn that hasn't sent its first
   request (StateNew) as active for 5 s.
   - Such a conn is left whenever a LocalAPI request gives up, or gets
     another conn, while its own dial is still in flight. The dialed conn
     then goes to the pool unused.
   - Fix: new `internal/tsnetlc`. `Client(ts)` gives a node's shared
     LocalAPI client our own `http.Transport`; `Close(ts)` calls
     `CloseIdleConnections` and then `ts.Close`.
   - Used for mgmt's node, hostd's node, `tw up`'s join, and object
     nodes (two calls and one import in `netd/registry.go`).
   - `TestCloseDrainsLocalAPIConns` makes the stray deterministically (a
     request given up on during a slow dial). Plain `ts.Close` takes
     5.002 s and `tsnetlc.Close` 0.02 s.
2. **Reboot: about 30 s between service active and healthy.** In the
   hostd log, `tailnet node` came at +0.6 s and the next line at +31 s.
   - After a boot, the nodes start from tsnet's netmap disk cache (logged
     as "loaded netmap from disk cache; 0 peers"), so they are Running
     before control has sent any peers. hostd's first `GET /v1/healthz` to
     mgmt's node went out with no route.
   - That one request then waited out netstack's SYN retransmits (1+2+4+8+16 s).
     Its HTTP client timeout is 60 s, and the loop only retried after an
     error.
   - Fix: `internal/hostdrun/mgmtwait.go` `waitMgmt`. Each probe is bounded
     to 1 s and a new one is dialled, and the log line `mgmt reachable`
     gives ms and attempts. The first post-reboot run logged
     `attempts=2 ms=2269` with a 2 s bound.
   - `TestWaitMgmtRetriesAStuckProbe` covers it.
3. **`tw up`: control round trips.** The tsnet debug logs show that every
   tsnet start on production control does `GET /key`, ~0.44 s, then
   `RegisterReq`, ~0.43 s, even with saved state, then a map request,
   ~0.15 s. The two requests are on new connections, which is
   controlclient's behaviour inside Tailscale.
   - `tw up` starts tsnet twice: the join in the CLI, then mgmt in the
     supervisor. `--separate-host` adds a third, hostd's join.
   - Single-process, first up (3.55 s in total):
     - preflight plus mgmt key mint 0.97 s;
     - join 1.07 s;
     - join close 0.26 s (an in-flight netcheck);
     - spawning the supervisor 0.2 s;
     - mgmt's tsnet start 1.02 s.
   - The only big saving left is to skip the CLI join, for example by
     handing mgmt its single-use key in a 0600 file, as hostd's join file
     already does. That would save ~1.3 s. `login.go` says "the key is used
     in-process and never stored", so that is Ross's call, not done here.
   - Small fix: hostd polls its join file every 50 ms instead of 500 ms.
4. **Startup/shutdown visibility**:
   - `hostd.(*Host).Close` logs `host stopped` with per-phase ms
     (`hostd/closelog.go`).
   - The e2e-linux harness saves the VM's tailworks logs and the unit's
     journal next to the results (`*.logs.txt`), and records every `tw up`
     phase (`up_<phase>_ms`).
5. **Ruled out**:
   - warm-key minting: async, ~150 ms per key, not on the path;
   - the OAuth token fetch: one call;
   - DNS at boot: control answered at once after the boot;
   - network-online ordering: the service was active and the network up
     at +0.6 s.

## Before → after on real control (ubuntu24-arm64, tailnet 1)

| | before: master 0401f5c4, separate (runs 1, 3) | after: separate (runs 8, 9) | after: single-process (run 10) |
|---|---|---|---|
| `tw up --service` → ready | 5.89 / 5.52 s | 5.45 / 5.40 s | 3.84 s |
| `systemctl --user restart` returns | 5.38 / 5.35 s (0.34 s in 2 other runs) | 0.36 / 0.35 s | 0.34 s |
| restart → healthy | 8.06 / 8.02 s | 3.06 / 3.04 s | 1.44 s |
| reboot: service active / healthy | 6.86 / 38.4 s | 5.63 / 7.25 s, 6.65 / 7.75 s | 7.60 / 7.62 s |
| reboot → workload answers the Mac | 48.9 s | 17.8 / 18.3 s | 18.1 s |
| `tw down --purge --service` | 0.63 s | 0.53 / 0.49 s | 0.53 s |

Healthy now comes 0.02–1.6 s after the service is active. The rest of the
reboot number is the VM booting. The workload still answers the Mac about
10 s after healthy; that is the Mac client re-learning the node's
endpoints from control, which I didn't change.

## Found, not fixed

Setting `OmitAuth` on mgmt's LocalAPI client (ADR-019 rule 4) makes
mgmt's WhoIs calls ~25 ms faster on macOS. With that change,
`make e2e-tw-upgrade` failed 3 of 3 runs: after the good upgrade, the
embedded host deploys its saved assignment with the previous lease epoch,
gets `lease_lost` on the workload secrets, and the deployment stays
`failed`. It is a latent timing race in the upgrade restart, so
`tsnetlc` doesn't set `OmitAuth`. Filing an issue was refused by the
permission system; it is reported to the coordinator.

## Tests

- `make lint`: PASS.
- `make test` (mbp runner): PASS before the last change. Rerun: one unrelated race in `hostd` `TestTelemetry` (objects.go:402 awaitNode vs http.go:159 egressObjHandler; this branch touches neither), which passed 3× when rerun locally with `-race`.
- `make e2e-tw-upgrade`: PASS (`TestTwUpgradeSingleMachine` 70 s,
  `TestUpDownBudget` 4.5 s).
- `make e2e-linux LINUX_DISTROS=ubuntu24-arm64`: 10 runs, all PASS (above).
- New: `internal/tsnetlc` `TestCloseDrainsLocalAPIConns`, and
  `internal/hostdrun` `TestWaitMgmtRetriesAStuckProbe` and
  `TestWaitMgmtGivesUp`.
