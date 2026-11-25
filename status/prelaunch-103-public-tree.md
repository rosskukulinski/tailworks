# prelaunch-103-public-tree (issue #103)

Remove private agent-workflow residue from the public tree.

## What changed

- **`LINUX-PLAN.md` deleted.** Its definition-of-done items that code
  cites are now `docs/install-testing.md`, "Linux requirements" (numbered
  1–7, same numbering). `cmd/tw/doctor_linux.go`, `cmd/tw/service.go`,
  `e2e/linux/linux_test.go`, `security/RED-TEAM.md` and
  `security/attacks/rt03_node_mgmt_socket_takeover.md` cite it there.
- **Dangling `BUILD-PLAN` references** (that file was removed before
  publication) in `e2e/{mac,mbp,multi,real,real/mgmt}`, `security/RED-TEAM.md`
  and `security/attacks/rt06_policy_json_tricks_test.go` now point at the
  new `docs/install-testing.md`, "Safety rules for the real-tailnet suites"
  (test tailnets only, additions-only policy writes with a backup, never
  print keys, clean up your own devices and keys). STATUS.md's header used
  to claim these rules were summarised in `security/THREAT-MODEL.md`, which
  wasn't true; it now points at the new section.
- **STATUS.md scrubbed** (frozen-history header kept): the author's home
  path is gone and the author's name in "personal tailnet not touched"
  notes is now "the maintainer". Test tailnet names (already public in the
  spec and e2e allow-lists) stay.
- **`scripts/check-secrets.sh` (run by `make lint`)** now also fails on:
  any `/Users/<name>` or `/home/<name>` path whose user isn't a placeholder
  (`me`, `u`, `x`, `you`, `user`, `runner`, `node`, …); and the
  private-repo push note LINUX-PLAN.md carried. Names that must never be published, and so
  can't be listed in the script either (a personal tailnet name), go in
  the untracked `.git/info/private-patterns`, one extended regex per line;
  the check reads it when present.

## Not changed

- `e2e/multimgmt/LEDGER.md` attributes design decisions to the maintainer by
  name ("Ross approved"); that's the public repo owner, not private data.
- `~/.config/tailnet-agents-design` stays: it's the Makefile's `TS_CREDS`
  default credentials directory, not a personal path.
- `security/LEDGER.md` loop-state headers stay (documented in
  `security/LOOP.md`).

## Tests

- The acceptance `git grep` from #103 (its phrase and home path): empty.
- `sh scripts/check-secrets.sh`: ok; with a temporary intent-to-add file
  containing a non-placeholder home path and the private-repo push note,
  it fails and names both lines.
- `make lint`: pass. `make changes-check`: ok (no upgrade-relevant paths).
- `make test`: pass (exit 0, 45 packages ok).
