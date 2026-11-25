Last update: 2026-11-24 11:30 PT. After review: `--apply-policy` is the only spelling and asks everywhere; deploy/policy apply write the caller grant for a new app. Part 3 (the stopwatch) still not done.

Last update: 2026-11-24 08:10 PT. `tw up --apply-policy` on Tailscale (issue #320 parts 1 and 2).

# feat-320-up-apply-policy

Issue #320: collapse the quick start's manual policy pastes, and measure
install to first fetch against ADR-021's 10-minute target.

## What's built

### One flag, one rule

`--apply-policy` exists on `tw up`, `tw tenant add` and `tw deploy` (and
`tw policy --apply`). Everywhere: tailworks shows the lines it will add as
a `+` diff, asks `Add these lines to the tailnet policy? [y/N]`, and
writes. `--yes` skips the question and is required without a terminal or
with `--json` (`confirmation_required`). Saying no is `cancelled`.

- `--policy-apply` is removed from `tw up` and `tw tenant add`, with no
  alias (owner's decision in review). Breaking; the fragment says so.
- `tw up --control=headscale` and `tw tenant add` used to write without
  asking. They now ask (`confirmBootstrapWrite` in
  `cmd/tw/up_policyapply.go`). This extension is the coordinator's reading
  of the owner's "I like prompt for apply-policy and then have --yes"; it
  is one call in each of `headscaleUpPolicy`, `cmdUp` and `cmdTenantAdd`,
  easy to take out.

### `tw up --apply-policy [--yes]` on Tailscale

`cmd/tw/up_policyapply.go`. In oauth mode, after the OAuth client
exchanges and the policy is read and before anything is minted, `tw up`
works out what the policy lacks of `operatorSnippet` (the three
`tagOwners` entries and the three grants), merges it, has the control
plane validate the result, shows the diff, asks, and writes with
`policy.ApplyRetry` (read with ETag, merge, validate, `If-Match` write, one
retry on a 412, then `policy_conflict`). mgmt isn't running yet, so `tw`
does this itself through `internal/policy`.

| Case | Result |
|---|---|
| Lines missing, confirmed | written; `tw up` goes on; `policyApplied: true` |
| Nothing missing | "nothing to write"; no validate, no write, no question |
| Prompt declined | `cancelled`; nothing written, nothing minted |
| No terminal (or `--json`), no `--yes` | `confirmation_required`, after the lines were shown |
| 403 on validate or on the write (no Policy File › Write) | a note says why; then as without the flag: `policy_missing_tag_owners` with the snippet if `tag:tw` doesn't own what this layout mints, otherwise `tw up` goes on and prints the missing grants at the end |
| Client can't read the policy | a note and the whole snippet; `tw up` goes on |
| 412 on the write | one retry; a second 412 is `policy_conflict` |
| The control plane rejects the merged policy | `policy_apply_failed` with the lines to paste; nothing minted |

Decisions that stand from the first round: owners of an existing tag are
never widened (`upPolicyWant`); the admin grant goes through
`policy.Options.AllowSrcs` for `autogroup:admin`; the whole snippet is
written in single-process mode too; validate before asking.

### The caller grant (`tw deploy --apply-policy`, `tw policy --apply`)

`mgmt/callergrant.go` (new). mgmt adds
`{"src": ["autogroup:member"], "dst": [<the workload node's tags>], "ip": ["tcp:80"]}`
to the snippet it writes, so the quick start's `curl` works without a
paste. The result's new `callerGrant` field names it; the CLI prints it in
the diff and adds "the autogroup:member grant lets every user in your
tailnet call <app>".

**The rule.** The grant is written when all of these hold, and otherwise
not:

1. mgmt can read the policy, and the bundle has `tailnet.tags`.
2. No grant with a network part, and no `accept` ACL, whose source is
   anything other than `tag:tw*` tags already has one of the workload tags
   or `*` as a destination. (People already reach the app, or the admin
   has chosen who does. Tailscale's default allow-all policy is this
   case.) Here nothing is written and nothing is said.
3. Every workload tag is a tailworks tag (`policy.IsTwTag`) that isn't
   reserved (`tag:tw`, `tag:tw-mgmt`, `tag:tw-host`) or `tag:tw-public`.
4. Every workload tag is absent from the policy's `tagOwners` and is being
   added by this same write. So: only for an app that is new to the
   policy.
5. `policy.Check` accepts the grant with `AllowSrcs: [autogroup:member]`
   (Headscale's profile refuses autogroup sources, so not on Headscale).

When 3, 4 or 5 fails and nobody reaches the app, `manual` gets a
"callers: …" line with the grant to paste.

**Why rule 4 is stricter than what was asked** ("if no grant already lets
callers reach the tag, write it"). The destination tags are the one part
of the grant that comes from the bundle. Without rule 4:

- a bundle that names another app's tag as its own (`"tags":
  ["tag:tw-wl-payroll"]`) would open that app to every user of the tailnet
  if it had no grant from people yet (only app-to-app grants);
- an admin who removed the grant to close an app would get it back on the
  next apply.

A tag that this write creates can't be anyone else's. The cost: an app
whose tag was pasted by hand earlier doesn't get the grant; `manual` says
so and prints it.

**RT-06.** `policy.Check` still refuses non-`tag:tw*` sources by default.
The bundle's entries pass through `preflightSnippet` with the strict
options first; the caller grant is appended afterwards, and
`onlyCallerGrant` then requires that it is the only grant in the snippet
with a source outside `tag:tw*` before the snippet is written with the
relaxed options. `TestCallerGrantBundleSourcesStillRefused` checks that a
preflight grant from `autogroup:member`, `*`, `autogroup:tagged`, a group,
a user or a foreign tag is never kept.

**"If the app already declares who may call it some other way."**
`tailworks.jsonc` has no field for callers, and no template declares any.
The two other ways in are `public` routes (Funnel) and `tailnet.service`.
Neither changes the rule: a public app still gets the tailnet grant to its
own tag, and a Service app gets the grant to its node's tag while the
grant to its `svc:` name stays manual (callers' app capabilities go on
that one, which is the admin's to write).

**Opt-out, no new flag:** add your own grant to the app's tag before the
first deploy (rule 2), say no at the question, or deploy without
`--apply-policy`. `docs/tailworks-jsonc.md#the-caller-grant` says this.

### What is unsafe or limited about the caller grant

- **`tw deploy --apply-policy --yes` in CI now makes every new app
  reachable by every user of the tailnet.** Before, a new app was
  reachable by nobody until an admin wrote a grant. An app that relies on
  "nothing reaches me without a grant" instead of checking its caller is
  exposed to the whole tailnet by default. The diff and the sentence are
  only seen by someone who reads the output.
- **Only `*` and the tag itself count as "already reached".** A broad
  destination written another way (an `autogroup:` destination, a host
  alias, an IP range) isn't recognised, so the grant can be written next
  to a rule that already covers it, and would then outlive that rule.
- **The rule is decided on the policy as planned.** `ApplyRetry` re-reads
  and re-merges on a conflict, but doesn't re-decide: a narrower grant an
  admin saves between the plan and the write doesn't stop the
  `autogroup:member` grant.
- **Object classes with their own nodes are not covered.** The grant
  targets the workload node's tags, as the README paste did. Calling an
  object on its own node still needs a grant to its class tag.
- **Legacy `acls` are now read by mgmt's policy type** (a new `ACLs`
  field), only for rule 2. Nothing else uses it.

### The quick start

One manual policy paste is left: `"tag:tw": ["autogroup:admin"]`, before
the OAuth client exists (the console only offers existing tags). README
step 2 says "This is the only line you paste". Steps 4 and 5 are `tw up
--apply-policy` and `tw deploy --apply-policy`.

### Docs

`README.md` quick start steps 2 to 6; `docs/INSTALL.md`;
`docs/tailworks-jsonc.md` (new "The caller grant" section);
`docs/troubleshooting.md`; `docs/headscale.md`; `docs/teams.md`;
`docs/errors.md`; `docs/services.md`; `docs/design/RFC.md` (flag
spelling); `api/openapi.yaml` with `make api`; `docs/cli.md` (`make
cli-docs`); `security/LEDGER.md` C12, `security/THREAT-MODEL.md`, and a
note on the RT-06 row of `security/RED-TEAM.md` (its text is history and
keeps the old spelling). `docs/security-model.md` doesn't state the
`tag:tw*`-only rule, so it is unchanged.

`git grep policy-apply` outside history (CHANGELOG, `status/`,
`STATUS.md`, `changes/released`, the release catalog) now finds only the
fragment, the tests that check the old flag is refused, and
`security/RED-TEAM.md`.

## Tests

Commands run on 2026-11-24 on the final tree (commit 3e42bc44, with
`origin/master` at v0.5.0 and #352 merged in).

| Command | Result |
|---|---|
| `make lint` | exit 0 |
| `make changes-check` | ok (needs: protocol; found in `changes/feat-320-up-apply-policy.md`) |
| `make api`, `make cli-docs` | generated files committed; `api-check` and `TestCLIReferenceUpToDate` pass |
| `sh ~/.claude/scratch/mbp-test.sh test` (`make test`, `-race`, on mbp) | exit 0: 127 Go packages ok, none failed (cmd/tw 288 s); JS tests pass |
| `go test ./cmd/tw/ ./mgmt/ ./internal/policy/ ./docs/... ./security/... -count=1` (local) | ok |
| `make e2e-headscale` (real Headscale v0.29.7 in Docker) | PASS, 69.9 s; `TestTwUp` now passes `--apply-policy --yes` |
| `make e2e-upgrade` (the spec changed) | PASS, 36.2 s |

Tests added or changed:

- `mgmt/callergrant_test.go`: `TestCallerGrantRule` (the rule
  above, including reserved tags, a defined tag, allow-all grants and
  ACLs, Headscale), `TestCallerGrantBundleSourcesStillRefused`,
  `TestPolicyApplyCallerGrant` (the endpoint: planned, written, audited;
  a second app on the same tag gets none; not put back after the admin
  removed it).
- `mgmt/policyapply_test.go`, `mgmt/policyapply_service_test.go`: the
  written snippet now includes the caller grant.
- `cmd/tw/policyapply_test.go`: the diff has the grant and the sentence;
  nothing is left to paste afterwards.
- `cmd/tw/up_policyapply_test.go`: as before, plus
  `TestApplyPolicyFlagSpelling` (`--policy-apply` is `bad_request` on `tw
  up` and `tw tenant add`) and `TestHeadscaleUpPolicyConfirm`.
- `cmd/tw/up_policyapply_ext_test.go` (from `TestCLIOAuth`, real binary).

Not covered by a test: `tw tenant add --apply-policy`'s prompt through the
command (it calls the same helper as the two tested paths); a successful
`tw up --apply-policy` write followed by a full start through the real
binary; anything against a real tailnet (does Tailscale accept and apply
the `autogroup:member` grant as written, and what a client without the
write scope gets back).

Not run: `make e2e-oauth` and the other real-tailnet suites; `make
e2e-tw-upgrade`.

## Not done: part 3 of the issue (the stopwatch)

No measurement was made, and no number here is an estimate. A real run
needs:

- **A release that has these flags.** `curl … get.sh | sh` installs the
  published release, where `tw up --apply-policy` doesn't exist on
  Tailscale and deploy writes no caller grant.
- **A fresh macOS VM.** None exists here: `tart list` shows one unrelated
  VM and no macOS one, and Lima only runs Linux guests. A fresh Linux VM
  is possible (Lima is installed).
- **A tailnet whose policy lacks tailworks' lines.** The dedicated test
  tailnet's policy already has them, so `--apply-policy` would report
  "nothing to write". Taking them out first is a removal, which the
  safety rules for the real-tailnet suites don't allow
  (`docs/install-testing.md`: additions only).
- **A person at the admin console.** The stopwatch includes creating the
  OAuth client and the one paste.

Nothing on a real tailnet was touched by this branch.

## Gaps and follow-ups

- Part 3 above.
- The caller-grant limits listed above.
- `tw up --apply-policy` doesn't write `autoApprovers` for `svc:tw-mgmt`
  (`docs/services.md`).
- `policy.MergeWith` rewrites the bytes of the policy slice it is given
  when it formats the result. `up_policyapply.go` passes clones. Other
  callers were not checked.
