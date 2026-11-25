---
kind: fix
upgrade: []
---
The first `tw up` on a terminal now shows how to create the OAuth client
before it asks for the secret: the `"tag:tw": ["autogroup:admin"]` line to
add, the Access controls and Trust credentials console URLs, and which
scopes to pick (scopes can't be changed on an existing client). Without a
terminal, `mint_mode_required` shows the same steps.

`tw host add`'s `policy_missing_tag_owners` snippet now names only the
missing `tag:tw-host` entry, as one line to put inside the existing
`tagOwners` section.

`tw custody migrate` no longer needs `--to setec` (setec is the only
target): `tw custody migrate --setec-url <url>`. `--to setec` is still
accepted. `tw up --custody setec` on a file-custody install prints the
exact migrate command, with its setec URL.
