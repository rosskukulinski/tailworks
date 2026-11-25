---
kind: feature
upgrade: []
---
`tw <command> --help` lists the everyday flags first and the rest under
headings: `tw up` groups its flags (Control plane, Store and custody,
Retention, Layout and identity, Isolation and egress, Tuning, Development
and testing), and `tw deploy`, `promote`, `rollback`, `dev`, `link` and
`ls` put the flags most runs don't need under "Advanced". `--state-dir`,
`--json` and `--tenant` are printed once, on a closing `global:` line,
instead of in every flag list, and usage lines no longer repeat
`[--tenant <tailnet>]`. A few test-harness flags (`tw up --tsapi-url
--local-dev --allow-sibling-egress`, `tw link --tsnet-dir`, `tw host setup
--firecracker --jailer`) are named on one "also accepted" line instead of
being described. Every flag is still accepted and still completes; nothing
was removed. `tw dev`'s usage line now shows `--once` and
`--test-scheduled`; `tw logs`' leads with `-n`, `-f`, `--errors` and
`--console`. Every `--yes` has the same wording.
