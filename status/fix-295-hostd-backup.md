# fix-295-hostd-backup

Part of #295, item 1: `tw backup` copies `hostd/` (Durable Object storage,
`fs` R2 bytes) consistently while tailworks runs.

## Built

- **On by default**, `tw backup --hostd=false` to skip
  (`cmd/tw/backup_hostd.go`, wired into `cmdBackup`/`cmdRestore`). A
  backup that silently left out every workload's data was the bigger
  hazard; the copy is online and needs no quiescing, so there is no
  downtime cost to defaulting it on.
- **DO storage:** every `<hostd>/tenants/*/workloads/*/do` tree. Each
  SQLite file (object databases, facets, `metadata.sqlite`) is copied
  with `VACUUM INTO` through a `mode=ro` connection; `-wal`/`-shm` are not
  copied, other files (`.facets`) are copied as they are. workerd opens
  these through SQLite's native VFS, so its POSIX locks and WAL index are
  shared with tw's connection: the copy is one commit and workerd keeps
  writing. No hostd endpoint was needed (tw runs as the same user on the
  same machine). Files deleted mid-walk are skipped.
- **R2 bytes (`fs`):** blobs are immutable (temp file + rename, never
  rewritten, deleted by GC). Copied before mgmt's snapshot (keeps blobs
  that GC deletes later) and again after it (blobs written meanwhile).
  Hard-linked when on the same file system, copied otherwise; `.put-*`
  temp files skipped.
- **backup.json** has `hostd: {workloads, databases, files, r2Blobs,
  r2Bytes, skipped}`; `notIncluded` now lists microVM `do.img` volumes,
  Node.js workloads' data dirs, and the rest of `hostd/`.
- **tw restore** (when the manifest has `hostd`): moves every current DO
  tree and `hostd/r2` into the safety backup, puts the backup's back, and
  writes a `backup.json` into the safety backup so restoring it undoes the
  restore, hostd included. Workloads created after the backup lose their
  DO storage to the safety backup too, so a re-created workload starts
  empty. `--out` inside `<state>/hostd` is refused.
- Docs: `docs/INSTALL.md` "Back up a running install" and "Restore",
  `docs/errors.md` (`backup_failed`), `docs/cli.md` (`make cli-docs`),
  `changes/fix-295-hostd-backup.md`.

## Tests

- `TestBackupHostdWhileWriting` (cmd/tw, real workerd): four SQLite DOs
  write a padded row and a counter in one transaction, nonstop, while R2
  blobs are put and GC-deleted; `tw backup` runs in the middle (60-70 DO
  writes land during it). Every backed-up database passes
  `integrity_check` and has count(log) = max(n) = c.n; every blob is
  whole; the manifest lists them. After more writes, `tw restore`, then
  workerd on the restored dir serves exactly the backed-up counters; the
  safety backup holds the replaced trees and says so. With a plain file
  copy instead of `VACUUM INTO` the test fails (`no such table: c`).
- `TestBackupHostdOff`: `--hostd=false` writes no `hostd/`, its restore
  leaves DO storage alone; `--out` inside hostd is refused.
- `backupRestoreRunning` (in `TestCLISingleProcess`) now checks the
  manifest's `hostd` lists the deployed workloads.

## Gaps

- An R2 object written and then overwritten or deleted within the backup
  itself can be restored without its blob (documented). Closing it needs
  a GC hold during the backup (a hostd hook).
- microVM DO volumes (`do.img`) and Node.js `TW_DATA_DIR`s still need
  `tw down` to copy; other hosts' `hostd/` isn't covered by `tw backup`.

## Results (2026-11-23)

- `make lint`: ok (local, after merging origin/master at 32410fed).
- `make changes-check`: ok (not upgrade-relevant; fragment added anyway).
- `make test` on mbp (`mbp-test.sh test`): ok, every package, before the
  merge of master (#296, #299: unrelated files, clean merge).
- After the merge, locally: `go test ./cmd/tw -run 'TestCLIReferenceUpToDate|TestBackup'` ok.
- `TestBackupHostdWhileWriting` locally ×5 with real workerd: ok.
- `make cli-docs`: docs/cli.md regenerated.
- No suite in AGENTS.md's table covers `tw backup`; none skipped.
