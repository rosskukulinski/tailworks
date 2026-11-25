---
kind: fix
upgrade: [protocol, config-default]
---
`tw policy` is easier to act on:

- `tw policy --apply` and `tw deploy --apply-policy` now handle a
  `tailnet.connects` target that is another tailworks app's tag before that
  app is deployed (say `tag:tw-wl-mcp`). Before, the tag had no `tagOwners`
  entry, so tailworks left the grants to it for you to add, and the snippet
  `tw policy` printed was rejected by the control plane (a grant to an
  undefined tag). Now tailworks adds the tag's `tagOwners` entry (the one
  deploying that app would add) and writes the grants; the printed snippet
  includes it too. Tenants with their own owner tag (teams) still define
  such tags themselves: the tag may be another team's.
- `tw policy --apply` prints one report: the checks, what it will add, and
  after writing either "run tw deploy" or what is still missing. It no
  longer prints the whole report a second time or shows policy ETags.
- What tailworks won't write is listed once, as a short reason, and stays in
  the snippet to paste.
- An app whose workload and object tags both need the same grant gets one
  grant with both sources, not one per tag. `tw policy` also names which
  node carries each of the app's tags.

The preflight result has a new `undefinedConnects` field.
