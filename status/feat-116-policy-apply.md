Last update: 2026-11-21 PT. `tw policy --apply` and `tw deploy --apply-policy` are built and tested, including a live run on tailnet 1.

# Issue #116: tw policy --apply

## What's built

- `POST /v1/tenants/{id}/workloads/{workload}/policy` (`mgmt/policyapply.go`,
  admin-only, audited as `policy_apply`):
  1. Preflights the bundle, then builds a `policy.Snippet` by parsing the
     preflight's own snippets, so what gets written is what `tw policy`
     printed.
  2. Each entry must pass `policy.Check` (RT-06). Entries that fail, a
     Service's `autoApprovers`, team ownership problems and grants to an
     undefined dst tag go into `manual`.
  3. Writes with `policy.ApplyRetry`: read with the ETag, merge additively,
     validate, then write with If-Match. On a 412 it retries once, and a
     second 412 is `409 policy_conflict`.
  4. `dryRun=true` only plans.
- `internal/policy/apply.go`: `ApplyRetry`, `ErrConflict`,
  `IsPreconditionFailed`, `IsForbidden`, `Snippet.Diff`.
- CLI (`cmd/tw/policyapply.go`): `tw policy --apply [--yes]` and
  `tw deploy --apply-policy [--yes]`. They plan, print the `+` lines and
  ask on a terminal. Without a terminal or with `--json` and no `--yes`,
  they stop with `confirmation_required`. Then they apply and re-check.
  `tw deploy --json` gains a `policy` event.
- New error codes: `policy_conflict` and `policy_write_denied`.
  `policy_apply_failed` can now also come from mgmt.
- Docs: README steps 2 and 4, INSTALL scopes, the tags section of
  docs/tailworks-jsonc.md, and docs/errors.md (including the `acl_denied`
  Fix line). `make cli-docs` and `make api` were rerun.

## Decision recorded

Closed #73 chose print-only for deploy plans. Ross has now asked for
apply, so apply is opt-in (`--apply` / `--apply-policy`) and print mode
stays the default and the fallback.

## Tests

- `go test -race ./internal/policy/ ./mgmt/ ./cmd/tw/`: ok.
  - New: `TestApplyRetryConflictOnce`, `TestApplyRetryConflictTwice`,
    `TestApplyRetryHostile`, `TestSnippetDiff`, `TestPolicyApply`,
    `TestPolicyApplyConflict`, `TestPolicyApplyHostile`,
    `TestPolicyApplyAdminOnly`, `TestCLIPolicyApply`.
- `make lint`: ok.
- `make test`, run locally because both mbp slots were busy: every package
  passed except `TestTenantRolesConsistent`. It failed because the new
  route had to be listed as admin-only. After fixing that, `./mgmt/`
  passes.
- Live, tailnet 1, scratch `TW_STATE_DIR` under /tmp:
  - Setup: backed up the policy, added the operator snippet with If-Match,
    and created a throwaway OAuth client (auth_keys, devices:core,
    policy_file; tag:tw).
  - `tw init` with the `hello` template, then `tw policy --apply --yes`:
    it wrote the tagOwners entry. It left the `tag:db` grant as manual,
    because tailnet 1 doesn't define `tag:db` and Tailscale rejects grants
    to undefined tags. `tw deploy` then stopped with `acl_denied`, as
    expected.
  - `tw init` with the `agent` template, `tw policy --apply --yes`, then
    `tw deploy`: all three tags were written, the deploy succeeded, and the
    node was ready in 2.0s.
  - Cleanup: `tw down --purge`, deleted the leftover mgmt device and the
    OAuth client, and restored the policy backup byte-for-byte (the ETag
    matches the original).
  - Still open: three unused warm-pool auth keys
    (`tw pool tn_tail7906d5`). Deleting them was refused by the agent's
    permission system.

## Gaps

- `autoApprovers.services` (for `tailnet.service`) is not written; it
  stays manual. Writing it would widen what `policy.Check` allows.
- The `hello` template's `connects: ["tag:db"]` means the issue's literal
  acceptance (`tw init x && tw policy --apply && tw deploy`) passes only on
  a tailnet that defines `tag:db`.
