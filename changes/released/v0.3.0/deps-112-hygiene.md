---
kind: security
upgrade: []
---
tw-mgmt's API request validation uses kin-openapi v0.149.0 (was v0.142.0),
which fixes eight advisories that govulncheck found reachable from tw-mgmt,
among them unauthenticated panics on a malformed request
(GO-2026-6783, GO-2026-6112). How tailworks tracks and ships
fixes for advisories in what it bundles (Go modules, workerd, Tailscale,
Firecracker, the guest kernel) is now written down in
docs/RELEASING.md, "Upstream advisories".
