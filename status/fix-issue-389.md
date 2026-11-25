Last update: 2026-11-24 PT. Issue #389: paste-safe tagOwners fix; allow-all tailnets get only the admin app grant.

## What changed

- `policyMissingTagOwners` takes the policy when tw read it. `tagOwnersFix`
  (`cmd/tw/oauth.go`) then prints only the entries to put inside the
  existing `tagOwners` section, never a second `"tagOwners"` key or a
  `tag:tw` line the policy already has. An existing tag keeps its owners
  plus `tag:tw`. Without the policy it prints the whole block.
- `snippetLines` starts every printed snippet with a HuJSON comment saying
  to merge into the existing section of the same name.
- The preflight's `policy_missing_tag_owners` also suggests
  `tw up --apply-policy`.
- `policy.AllowsAll` (`internal/policy/allowall.go`) detects an
  unconditional allow-all grant (`*`/`*`/`*`) or ACL (`accept`, `*`,
  `*:*`). `operatorSnippetFor(pol)` drops the network-only host ⇄ mgmt
  grants then; the admin grant (app capability) stays. Used by
  `tw up --apply-policy` and the end-of-`tw up` grants check.
- `docs/errors.md` `policy_missing_tag_owners`: merge into the existing
  section.

## Tests

- `make lint test`: pass (after merging origin/master at 97b05a04).
- `make changes-check`: ok (no upgrade-relevant paths).
- New: `TestAllowsAll`, `TestUpApplyPolicyAllowAll`,
  `TestTagOwnersFixPasteSafe`.
- Not run: `e2e-headscale`, `e2e-real` (Headscale policy unchanged; real
  tailnet needs credentials).

## Gaps

- The issue title mentions `--policy-apply`; the flag is `--apply-policy`
  (an unknown flag is rejected), so no change there.
- Headscale's profile snippet still includes the network grants.
