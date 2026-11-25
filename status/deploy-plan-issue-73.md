Last update: 2026-11-16 PT. `twd policy`: the whole tailnet policy an app needs, before deploying (#73).

# twd policy (#73)

## What's built

- `twd policy [dir]` (`cmd/twd/policyplan.go`):
  - It builds the bundle and runs the dry-run preflight that `twd deploy`
    runs (`POST /v1/deployments?dryRun=true`). Nothing is created.
  - It prints one line per check: tagOwners, `tailnet.connects`, service,
    public, and callers (not checked).
  - Then one snippet: the missing `tagOwners`, the `grants` (the
    `tailnet.connects` ones, plus the caller grant), the `autoApprovers`
    and the `nodeAttrs`.
  - It exits `acl_denied` (or `funnel_required`) while a checked part is
    missing.
  - With `--json` it returns `$defs/policy`. When something is missing,
    the error's `fix` carries the snippet.
  - When mgmt can't read the policy (no Policy File › Read scope), it says
    so, lists the tags, and still shows the caller grant.
- I made it a command rather than `twd deploy --plan`, as the issue allowed,
  so its `--json` has its own schema `$def` and `deploy`'s stays as it is.
  It's one entry in `commands`, in the Deploy group.
- `twd init` now suggests `twd policy && twd deploy`.
- Docs updated:
  - README steps 4 and 5 use `twd policy`: one policy edit, then deploy.
  - The `twd-deploy` skill says to run `twd policy --json` before an app's
    first deploy.
  - `docs/cli-json.md`, `docs/wrangler.md`, `docs/twd-jsonc.md`.
  - `docs/cli.md`, regenerated.

## Tests

- `TestCLIPolicyPlan`, against a real twd-mgmt and a fake policy API:
  - nothing in the policy: `acl_denied`, and the snippet has the
    tagOwners, the connects grant and the caller grant;
  - with `--json`, the error's fix has the snippet;
  - nothing is deployed;
  - a complete policy: exit 0, with the caller grant still shown;
  - `--json` on success matches `policyResult`.
- `TestCLIJSONContract` has a `policy` entry. `TestHelpGroups` and the
  CLI reference test pass.

## Not done

- The issue's `docs_policy_test.go` check of the README snippet against
  `twd policy` output for the hello template. The README no longer shows
  per-app tagOwners, so there's nothing for it to compare.
