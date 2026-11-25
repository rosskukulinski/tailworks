---
kind: fix
upgrade: []
---
`tw up`'s tagOwners fix is now safe to paste (#389). A tailnet with an
OAuth client already has a `tagOwners` section, so the old fix, a whole
second `"tagOwners": {…}` block that also repeated `tag:tw`, made the policy
fail to parse. When tw could read the policy, it now lists only the entries
to put inside the existing section; a tag that is already there keeps its
owners and gains `tag:tw`. Every printed snippet starts with a comment
saying to merge each entry into the existing section.

On a tailnet whose policy still allows all traffic (the default allow-all
grant or ACL), `tw up --apply-policy` and `tw up`'s policy check no longer
want the host ⇄ mgmt network grants: only the tagOwners and the admin
console's app grant are added. If the allow-all is narrowed later, `tw up`
reports the network grants as missing.
