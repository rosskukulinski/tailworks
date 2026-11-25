Last update: 2026-11-22 13:50 PT. Re-ran e2e-linux ubuntu24-arm64 after #250: tw down fixed, up, restart and reboot-to-healthy still slow (#245 stays open).

# test-245-rerun (issue #245)

- Build: `make release RELEASE_TARGETS=linux-arm64 RELEASE_VM_ASSETS=0` at
  master `0401f5c4` (after #250), giving `tailworks-0.3.0-0401f5c4-linux-arm64.tar.gz`.
- Run: `scripts/e2e-linux.sh ubuntu24-arm64` with `TW_E2E_VM_PREFIX=twdl-`
  and that tarball, on test tailnet 1, OAuth only, sandbox on, with the
  reboot and `--ephemeral` phases. **PASS**.

| | `45cd9203` (before) | `0401f5c4` (after #250) |
|---|---|---|
| `tw up --service` → ready | 5.98 s | 5.80 s |
| `tw down --purge --service` | 6.69 s | **0.60 s** |
| `systemctl --user restart` command | 5.2 s | 5.28 s |
| restart → healthy / answers | 7.9 / 10.9 s | 7.96 / 10.99 s |
| reboot → active / healthy / answers | 5.6 / 36.6 / 47.1 s | 6.8 / 37.4 / 48.0 s |

- Cleanup: 22 run devices deleted, 0 left; 0 tw keys and 0 of 36 warm
  keys valid after down; VM clean and stopped; no devices left on the
  tailnet.
- #245: numbers posted as a comment; it stays open, because reboot →
  healthy is still 37 s against the 6.6 s target.
- `make lint`: PASS (docs and status only; `make test` not needed, no code changed).
