---
kind: feature
upgrade: [cli-json]
---
`tw doctor` now prints a short summary instead of one long paragraph per
check. It starts with how many checks failed, warned, passed and were
skipped. Failures and warnings come next, each with its fix, and the
passing checks are folded into one line per group (Install, Tailnet,
Storage, Isolation, and Linux host on Linux). Long lines wrap to the
terminal width, and your home directory is shown as `~`. `tw doctor -v`
prints the full detail of every check, as before. `tw doctor --json` is
unchanged, except that each check now has a `group`.

In single-process mode, missing `tw-mgmt` and `tw-hostd` binaries are now
reported as info rather than a warning, because they aren't needed there.
