# fix-611-exec-stdout-lost

Issue #611 (K-F5 in `e2e/dataplane/ledger/containers.md`): a container exec
on Firecracker answered exit 0 with empty stdout.

## Cause

`tw-guestd` `pumpExec` sets a 200 ms read deadline on the exec's stdout and
stderr pipes once the process exits (a daemon the exec left behind may hold
them open). Go's poller fails a read whose deadline has passed without
trying the read, so a pump goroutine that hadn't got to the pipe within
200 ms of the exit (a 2-vCPU nested-KVM guest just after boot, ~9
concurrent execs) dropped everything the process had already written. The
exec then sent its exit frame with no stdout frames. The host side
(`hostd/vm/container.go` demux, `hostd/containers.go` `serveExec`) keeps
frame order and was not at fault.

## Fix

`guest/tw-guestd/execpump_linux.go` `execPumpOutput`: when the deadline
hits, clear it and drain what is buffered in the pipe with non-blocking raw
reads before stopping. Output written after the exit (a leftover daemon) is
still cut off.

## Tests

- `TestExecPumpDeadlineKeepsBufferedOutput` (37 B, 32 KiB, 60 KiB buffered,
  deadline already expired, writer held open): fails on the old loop
  ("pump sent 0 bytes"), passes with the fix. Plus deadline-cuts-off-daemon
  and EOF cases. Run in Lima `twd-dev` (`go test -race ./guest/...`: ok).
- `make lint`: ok. `make test` (offloaded to mbp): ok.
- Firecracker (twd-amd64, guest image rebuilt from this branch), `restore`
  seed 10, 4 min, 2 hosts: K1 clean with no "200 without a report" notes
  (both earlier runs of seed 10 had them). K2 failed on K-F4 (#610, a pool
  node left `minted` after a burst-crash), unrelated. Results:
  `20261125T200002Z-k-restore-10` on twd-amd64.
