---
kind: feature
upgrade: [protocol]
---
Every policy snippet tailworks prints for you to paste now has the same
shape and starts with the same line: `// merge into your policy: put each
entry inside the existing section of the same name (don't add a second
one)`. That covers `tw policy`, the `tw deploy` preflight (on the terminal
and in the `acl_denied` / `funnel_required` fix), and `tw up`. Before, only
`tw up` printed it, and a block pasted next to an existing `"tagOwners"` or
`"grants"` section left the policy unparseable. `tw deploy`'s failure fix
is now one block instead of one per section, and the public-ingress
`tagOwners` entry no longer shows up twice in it.

Grants are printed as `{ "src": […], "dst": […], "ip": […] }` everywhere,
the policy-apply diff and `tw up`'s diff included (they used to be
key-sorted and unspaced). On a Headscale tailnet, `tw host add`'s
`policy_missing_tag_owners` snippet now offers `"tag:tw-host": ["tag:tw"]`
instead of `autogroup:admin`, which Headscale rejects.

The mgmt API's deploy preflight (`PreflightResult`) has a new `snippet`
object: everything missing as one typed policy snippet. The existing
string fields are still there, rendered from it. `missing[]` entries also
have a new `ip` field. The policy-apply answer's `callerGrant` uses the
same `{ "src": …, "dst": …, "ip": … }` form. A newer `tw` reading an
older mgmt still falls back to the string fields.
