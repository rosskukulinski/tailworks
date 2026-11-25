---
kind: breaking
upgrade: [cli-json]
---
`tw docs` now carries the guides only (`docs/*.md`, the README, CHANGELOG
and SECURITY). The API contract (`docs/contracts/`) and the design records
(`docs/design/`, the RFC and the ADRs) are no longer built into the CLI:
`tw docs show design/…` or `contracts/…` answers `not_found` with the
GitHub URL of the file in its fix. The `tw` binary is about 0.7 MB smaller.

`tw link` and `tw whoami` print the admin console URL (`console
<endpoint>/ui/`) and return it as `console` in `--json`. `tw whoami` prints
it only to an identity the console admits: an admin or tenant admin gets
`/ui/`, an approver who is neither gets the approvals page
(`/ui/approvals`). Neither prints it for a `tw link --tsnet` machine, whose
browser can't reach mgmt.
