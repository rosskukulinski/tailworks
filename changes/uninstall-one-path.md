---
kind: feature
upgrade: [cli-json]
---
`tw uninstall` removes tailworks from a machine in one command, where it
used to take five. It kills every object and deletes its device,
deregisters this host (separate-host layout), stops tailworks and removes
its login service, deletes mgmt's own node with the tenant's credential,
removes tailworks' skills from the coding agents it finds, removes the
install (or prints `npm uninstall -g tailworks`) and deletes the state dir,
`backups/` included. `--keep-state` keeps the state dir's data, and on a
linked developer machine it removes `link.json`, `cli/` and the install.
It prints what it will do and delete and asks first (`--yes` without a
terminal, `--dry-run` to only look). It refuses while the microVM tier's
helper is installed, while mgmt isn't healthy (`--force` skips the purge)
and while other hosts are registered. New error code: `uninstall_blocked`.
`install.sh --uninstall --purge` now removes the whole prefix, so both
ways end in the same place.

`get.sh` and `install.sh` refuse to install another version over a
running tailworks (no backup, health check or rollback that way) and name
the `tw upgrade` command instead. A same-version re-run (a repair) still
goes ahead; `--force` (`TW_INSTALL_FORCE=1` for `get.sh`) swaps anyway.

Operator action: a script that re-ran `get.sh` or `install.sh` over a live
install to upgrade it now fails: use `tw upgrade`, or `tw down` first, or
pass `--force` / `TW_INSTALL_FORCE=1`.
