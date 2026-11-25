Last update: 2026-11-15 PT. `twd dev` and the test helpers clean up after SIGKILL (issue #44).

# Orphaned workerd and leaked /tmp/twd-dev-* after SIGKILL (#44)

## What's built

- `internal/devreap` (new). `Guard(dir, opts)` writes `<dir>/.twd-owner.json`
  (owner pid, extra program names, keep flag) and, on first use, starts one
  watchdog per process: a re-exec of the current binary (`__twd-devreap`,
  caught in package `init` like procsb's launcher, so it works in test
  binaries too) in its own session, stdio on /dev/null, holding the read end
  of a pipe. Guarded dirs are sent down the pipe one per line. When the
  owner exits, however it exits, the watchdog reads EOF and `Reap`s each
  dir: SIGTERM (to the process group when the pid leads one, as hostd's
  workerd and node do), 3 s grace, SIGKILL, then remove the dir unless
  `Keep`.
- A process belongs to a dir only if it runs as this uid, its argv[0] base
  name is `workerd`, `node` or one passed in `Options.Programs`, and its
  command line names a path inside the dir (or, for a node app whose args
  are all relative, its cwd is inside the dir: `/proc/<pid>/cwd` on Linux,
  `lsof -d cwd` elsewhere). Listing is `ps -A -ww -o pid= -o uid= -o args=`
  on both OSes.
- `ReapStale(glob)` reaps dirs whose recorded owner pid is gone (ESRCH).
  Skips dirs with no owner file (older twd versions, or a dir being created
  right now), dirs owned by another uid, and dirs whose owner is alive
  (another worktree's `twd dev`).
- `cmd/twd/devreap.go`: `guardDevDir` = `ReapStale("/tmp/twd-dev-*")` then
  `Guard(tmp)`. `cmd/twd/dev.go` gets one call after `MkdirTemp`.
- `hostdtest.ShortDir` guards every dir it makes (covers `hostdtest.Start`
  and the ~30 other callers; `TWD_KEEP_INSTANCES` sets Keep).
  `nettest.StartWorkerd` guards its rundir with Keep (the dir is the
  caller's).

## Design choice

One mechanism on both OSes (pipe-EOF watchdog) instead of Pdeathsig on
Linux plus something else on macOS: it needs no change in hostd (no
production behaviour change, no hotspot edits), it covers node and anything
a node app spawned (process groups), and the same code is tested on both.
Pdeathsig was left out: it would only help on Linux, needs edits in
`hostd/workload.go` and `hostd/node.go`, and would change production hostd
(workerd dies with twd-hostd) which deserves its own decision.

## Tests

- `go test -race ./internal/devreap/` (macOS arm64, and the cross-compiled
  test binary on the linux/amd64 test VM): `TestGuardSIGKILL` (owner
  subprocess SIGKILLed: child exits, dir removed), `TestReapStale` (dead
  owner reaped; live owner, no-owner-file dir and a non-workerd process
  naming the dir all untouched), `TestGuardKeep`.
- `go test -run TestDevSIGKILL ./cmd/twd/`: real `twd dev` + workerd,
  SIGKILL, workerd gone and dir removed. Fails on master behaviour
  (checked by disabling the watchdog: "1 of workerd still running").
- Manual repro on macOS: see the PR.

## Known gaps

- Dirs leaked by older twd versions have no owner file and aren't reaped
  automatically (deliberately: nothing proves they're ours).
- If both `twd dev` and its watchdog are killed (a reboot, or someone
  SIGKILLs the watchdog too), cleanup waits for the next `twd dev` start.
- Production twd-hostd children are unchanged (a SIGKILLed twd-hostd still
  leaves its workerd; restart behaviour there is a separate question).
