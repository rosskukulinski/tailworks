---
kind: fix
upgrade: []
---
`tw deploy`'s `acl_denied` error now also says that tw can add the lines
itself: `tw deploy --apply-policy` (it shows them and asks first). When
the tenant's OAuth client can't write the policy, `tw deploy
--apply-policy` says so in one line and fails with `acl_denied` and the
snippet to paste, instead of `policy_write_denied`; with
`--allow-missing-grants` it deploys without the lines.

`tw upgrade` restarts `tw up` with `--apply-policy=false`, so an
unattended restart never touches the tailnet policy. `tw up --yes` is
documented as what it is: it skips the confirmation before
`--apply-policy` writes the policy.
