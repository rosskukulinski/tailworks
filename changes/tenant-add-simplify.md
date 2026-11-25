---
kind: breaking
upgrade: []
---
`tw tenant add` is simpler:

- It no longer takes `--registrations-per-minute`. Set the throttle after
  adding the tenant with `tw tenant set <tenant> --registrations-per-minute N`
  (the tenant starts at the default 200/min, and nothing registers before
  its first deploy). The flag was also silently ignored when the tenant
  already existed. Scripts that passed it now fail with "flag provided but
  not defined". The API field is unchanged.
- Without `--oauth-client-file`, on a terminal, it asks for the tenant
  tailnet's OAuth client secret the way the first `tw up` does and saves it
  to `~/.config/tailworks/<tenant ID>-oauth.env` (0600), reusing that file
  on later runs. This works only on the default state dir and without
  `--json` or `--owner-tag`; elsewhere the flag is still required. A pasted
  client that already serves the operator or another tenant is refused
  (`state_conflict`), and one that can't get a token isn't saved.
- It asks a tenant tailnet only for what it needs. On Tailscale it writes
  nothing: it checks that the owner tag (`tag:tw`, or `--owner-tag`) is in
  `tagOwners` and prints the line to add if not. It never adds
  `tag:tw-mgmt`, `tag:tw-host` or the host and mgmt grants (those nodes live
  on the operator's tailnet), and `--owner-tag` no longer adds
  `autogroup:admin` to an existing tag. On Headscale it wants only
  `"tag:tw": []`, which `--apply-policy` writes after asking.
