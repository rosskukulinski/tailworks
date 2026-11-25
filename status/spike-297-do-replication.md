Last update: 2026-11-24 15:30 PT. Spike #297 done: the answers and the recommendation for #38 are in `docs/design/adr/do-replication.md` (ADR-do-replication, Proposed).

# spike-297-do-replication

## Result

The pinned workerd (1.20261121.1) already has SRS-style replication in its
experimental cluster mode. It captures each transaction's WAL pages through
its VFS, holds the output gate until a bucket has the pages and the object's
epoch still holds, breaks the gate and restarts the object on failure, and
restores one object from snapshot plus log before it opens. The
recommendation for #38 is to build on it, opt-in for installs with an
S3-compatible bucket, rather than compacting the D1 journal. Five gaps are
listed in the note. The blocking one is that a cluster instance exits after
about 8 s of bucket trouble, and tailworks runs stateless Workers in the
same workerd process. The first step is a credential-free e2e harness
against a local conditional-write S3 server.

## What was run

- `git clone --depth 1 --branch v1.20261121.1 https://github.com/cloudflare/workerd`
  into a fresh directory outside the repo, read only (not built or run).
  `gh api` was used for the commit history of `src/workerd/server/cluster`
  (49 commits from 2026-10-29 to 2026-11-20, 41 of them by 1.20261118.1).
- `go build ./cmd/tw`, then `tw dev` on a scratch app outside the repo: a
  plain SQLite DO, a DO with its own tailnet identity, and a D1 binding.
  - The storage directory was listed while objects were live and after
    they went idle.
  - The WALs were parsed with a small script: 4 KiB pages, 54 frames and 2
    commits for 50 inserts.
  - Autocheckpoint: the WAL reset at 1215 frames (5,005,832 bytes) with a
    new checkpoint sequence and salts, and the file was not truncated.
  - The `-wal` and `-shm` files were deleted when the object closed.
- Not done: the optional prototype (a workerd cluster against a local
  S3-compatible server). Starting that server wasn't available in this
  session.
- `make lint-docs test-docs`: see the PR.

## Notes

- `docs:TestADRIndex` fails under `CI=true` while a slug ADR file exists
  (AGENTS.md rule 5). That is expected for a Proposed ADR on a branch, as on
  #226. The ADR gets a number when it merges.
