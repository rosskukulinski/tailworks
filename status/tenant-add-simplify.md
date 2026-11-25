# tenant-add-simplify

`tw tenant add` simplifications from the interface audit (#434): #537, #538,
#475, in one PR because all three edit `cmdTenantAdd`.

## What changed

- #537: `--registrations-per-minute` removed from `tenant add`
  (`tenant set` keeps it). docs/cli.md regenerated.
- #538: no `--oauth-client-file` on a terminal (default state dir, no
  `--json`, no `--owner-tag`/`--name-prefix`) asks for the secret with
  `promptOAuthSecret` (now parameterised with the command and tailnet) and
  saves `~/.config/tailworks/<tenant ID>-oauth.env` 0600; an existing file is
  reused, never overwritten. A pasted client whose ID is the operator's or
  another tenant's is refused (`state_conflict`), and one that can't get a
  token is removed. `tailscale` stays the default `--control`. Code:
  cmd/tw/tenant_oauth.go.
- #475: cmd/tw/tenant_policy.go. Tailscale tenants: read-only check that the
  owner tag is in `tagOwners`, paste line if not, never written (even with
  `--apply-policy`), never widened. Headscale: `"<owner>": []` only, behind
  the existing confirm. docs/teams.md, docs/INSTALL.md, docs/headscale.md.

## Not done

- #538 security amendment's optional `DiscoverTailnet` check (refuse a
  client whose tailnet differs from the positional) is not implemented.
- The client-ID reuse check runs only for a pasted secret: the hermetic
  multi-tenant test gives every tenant the same fake client ID, and a
  `--oauth-client-file` keeps today's path check.

## Tests

- `go test ./cmd/tw/ -run 'TestTenant|TestSubcommandHelp|TestApplyPolicyFlagSpelling'`: pass
- `make lint`: pass
- `make changes-check`: ok
- `make test`: all Go packages pass except `mgmt` TestWarmKeysRevokedOnCloseInParallel (timed out under load from parallel agents; passes on rerun alone, untouched code). test-js did not run after test-go stopped; no JS changed.
