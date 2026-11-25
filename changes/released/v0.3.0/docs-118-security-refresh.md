---
kind: security
upgrade: []
---
The security docs match the code again. `security/THREAT-MODEL.md` describes the
process-tier sandboxes as they are (Seatbelt on macOS; Landlock and seccomp on
Linux, which confine TCP only with Landlock ABI 4 or later) and adds the
boundaries for `/mcp`, public ingress, upgrades and the hosts' push API.
`docs/security-model.md` lists the `/mcp` and release-signature boundaries. Every
open item in `security/LEDGER.md` now has a GitHub issue with the `security`
label, and every claim the ledger marks pinned names a test that drives the
code tailworks runs. No behaviour changed.
