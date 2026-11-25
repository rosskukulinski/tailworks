Last update: 2026-11-23 PT. `tw policy --apply` writes `autoApprovers.services` (issue #295, item 3).

# Issue #295 item 3: `tw policy --apply` writes `autoApprovers.services`

Follows `status/feat-116-policy-apply.md`, whose "Gaps" listed this.

## What's built

- `internal/policy`: `Snippet` gains `AutoApprovers` (`autoApprovers.services`
  only; `internal/policy/autoapprovers.go`).
  - `Check` admits an entry only when every key is a valid `svc:` name and
    every approver is a `tag:tw*` tag (or `Options.AllowTags`). It refuses
    `*`, `autogroup:*` (including `autogroup:admin`), groups, users,
    other tags and empty lists. The Headscale profile refuses the field
    (Headscale has no Services).
  - `Missing`, `MergeWith`: additive, like tagOwners. They create
    `/autoApprovers` or `/autoApprovers/services` when absent, add a
    missing key, or append a missing approver to an existing key.
  - `VerifyAdditive`: `autoApprovers.routes`, `exitNode` and every other
    key must be unchanged. Existing services entries may only gain the
    snippet's approvers, and no entry may be added that the snippet
    doesn't contain.
  - The tagOwners code in `Missing`, `MergeWith` and `VerifyAdditive` now
    uses the same helpers (`missingOwners`, `ownersOps`, `diffOwners`).
    Behaviour is unchanged.
- `mgmt/policyapply.go`: `preflightSnippet` writes the preflight's
  `ServiceSnippet` through `serviceApprover` rather than always listing it
  as manual. The entry is written only when:
  - it has exactly one key, the workload's own Service;
  - every approver is one of the tags the workload node is deployed with
    (`PreflightResult.workloadTags`, an unexported field set by
    `servicePreflight`);
  - every approver is defined in tagOwners (in the policy or in the same
    snippet);
  - it passes `policy.Check`.
  Otherwise it stays manual, with the reason and the snippet. Examples:
  Headscale, an undefined tag, or a workload with no tags (the snippet's
  `tag:tw` fallback).
- Help text for `tw policy --apply` and `tw deploy --apply-policy`. Ran
  `make cli-docs`.
- Docs: `docs/services.md`, `docs/tailworks-jsonc.md`, `docs/INSTALL.md`,
  the `api/openapi.yaml` description (`make api`), and
  `security/RED-TEAM.md` (which said tailworks never writes it).
- `changes/fix-295-autoapprovers.md` (`upgrade: [protocol]` for the
  description-only spec change).

## Tests

- New tests: `TestMergeAutoApprovers`, `TestAutoApproversRefused`,
  `TestVerifyAdditiveAutoApprovers`, `TestAutoApproversHuJSON`
  (`internal/policy`); `TestPolicyApplyService` and
  `TestServiceApproverNarrow` (`mgmt`).
- `go test ./internal/policy/` and
  `go test -run 'PolicyApply|ServiceApprover|Service|Preflight' ./mgmt/`: ok.
- `make lint`, `make changes-check`: ok (local).
- `make test` on mbp (`mbp-test.sh`): every package passed except
  `cmd/tw`'s `TestDoctor`. It failed on mbp's `data_disk` warning
  (31 GiB free, under 10%), which is about that machine, not this change.
  `go test -count=1 ./cmd/tw/` locally: ok.
- `make e2e-upgrade` on mbp: see the PR.

## Not done

- No live run against a real tailnet. The fake control plane in
  `mgmt` tests doesn't validate `autoApprovers` semantics, only the
  merge.
- `tw up --policy-apply` still doesn't write `svc:tw-mgmt`'s
  autoApprovers (`docs/services.md`, multi-mgmt). That's a separate path
  (`InfraSnippet`) and is not part of this item.
