Last update: 2026-11-24 PT. Issue #481 item 5: policy fix texts, write-denied fallback, `--apply-policy=false` on the upgrade restart.

# policy-fix-texts

First, decision-free part of #481 (the prompt-by-default change follows in
its own PR).

## What changed

- `preflightFailure` (cmd/tw/public.go): `acl_denied`'s fix ends with
  "or let tw add them (shows the lines and asks first): tw deploy --apply-policy".
- `tw deploy --apply-policy` (cmd/tw/deploy_policy.go, one call in app.go):
  `policy_write_denied` prints one line and falls back to the preflight's
  `acl_denied` snippet (without the "let tw add them" line, plus how to get
  a write-scoped client). With `--allow-missing-grants` it deploys without
  the lines.
- `tw up --yes` help: tenant add's wording ("skip the confirmation before
  --apply-policy writes the policy; …").
- `tw upgrade`'s restart of the new release runs
  `tw up --yes --apply-policy=false`. Restarting an old release (install
  failed, rollback) keeps the plain `tw up --yes`: v0.5.0's `tw up` has no
  `--apply-policy` flag.
- docs/errors.md `policy_write_denied`; docs/cli.md regenerated.

## Tests

See the PR's Tests section (commands and results).

## Test results (2026-11-24, machine at load average ~150)

- `make lint`: pass.
- `go test ./cmd/tw -count=1`: pass on the first run (82 s). A rerun
  after merging origin/master failed only on timing budgets under load
  (TestCLIEndToEnd/TestCLIOAuth `(e) kill` > 1 s, TestDevPython workerd
  health timeout). Nothing in them touches the policy step.
- `make test`: the first run failed on a full disk (ENOSPC in go build,
  sqlite "disk I/O error"). `go test ./mgmt` rerun alone: pass.
- `make e2e-tw-upgrade`: TestTwUpgradeSingleMachine PASS (the upgrade
  restart with `--apply-policy=false`, and both rollbacks to the old
  release). TestUpDownBudget FAIL: `tw up` took 2.08–5.6 s against a 2 s
  budget under load.
- New: TestDeployPolicyWriteDenied (cmd/tw/deploy_policy_test.go).
