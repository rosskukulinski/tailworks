Last update: 2026-11-24 PT. Onboarding hints from the simplify audit (#442, #443, #444).

## What changed

- **#442** `oauthConsoleSteps()` (cmd/tw/oauth.go) is now the one set of
  console steps for both the first-run prompt (`promptOAuthSecret`,
  cmd/tw/up_defaults.go) and `mint_mode_required`: the
  `"tag:tw": ["autogroup:admin"]` line inside the existing `tagOwners`
  section, the Access controls URL (`consoleACLURL`, new) and the Trust
  credentials URL (`consoleOAuthURL`), and the scopes with least privilege
  (Auth Keys › Write required; Policy File › Write for `--apply-policy`,
  Read to paste yourself; optional Devices › Core › Write and Services ›
  Write; scopes can't be changed on an existing client). The prompt adds
  the hidden-input line as step 4; `mint_mode_required` adds its
  no-terminal step and the snippet. No browser is opened. The step text
  says `tw up --apply-policy` writes the rest: the policy-write default is
  #481, not changed here.
- **#443** mgmt `CreateHost`'s `policy_missing_tag_owners` snippet
  (`HostTagOwnersSnippet`, replaces `TagOwnersSnippet`) is one line, the
  `tag:tw-host` entry with a "inside the existing tagOwners section" HuJSON
  comment. The optional "for tw host add later" label in the first-run
  prompt was not done: that prompt no longer prints the host lines.
- **#444** `tw custody migrate --setec-url <url>`: `--to` is out of the
  usage and docs; `--to setec` is still accepted (deprecated) so earlier
  scripts keep working, any other value is `bad_request`. tw up's
  file → setec refusal and `already_running` print the exact command
  (`custodyMigrateCmd`, cmd/tw/custody.go) with the URL given and
  `--state-dir` when it isn't the default.

## Tests

- `make lint`: ok.
- `make test`: failed in 18 timing-bound tests (mgmt placement "not live"
  hosts, hostd KV/R2/locks/public client timeouts, mgmtd start budget,
  cliflow kill latency) while the machine's load average was 90-130 on 12
  cores from other agents. Rerunning exactly those 18 with `go test -run`
  passed (mgmt, internal/mgmtd, hostd, cmd/tw: ok), as did
  `TestCLIOAuth`/`TestCLISingleProcess` (they exercise `mint_mode_required`).
- After merging origin/master again (e2e/dataplane only): `make lint` ok.
- `make cli-docs`: docs/cli.md regenerated.
- `make changes-check`: ok (no upgrade-relevant paths); fragment
  `changes/simplify-onboarding-hints.md` added anyway for the user-visible text.
- New: `TestOAuthConsoleSteps` (cmd/tw/oauth_steps_test.go); extended
  `TestUpStoreFlags` (exact migrate command) and mgmt's OAuth host test
  (only the tag:tw-host entry).
- Skipped: `make test-pg` (no store change), e2e suites (text-only changes;
  `e2e/cliflow` still finds `mint_mode_required` and `--oauth-client-file`).
