---
kind: security
upgrade: []
---
`tw status` and `GET /v1/status` no longer report Node-tier egress as `enforced` on a Linux host whose Landlock ABI is below 4 (kernels before 6.7, such as Ubuntu 22.04's 5.15). Landlock can't restrict TCP there, so egress is now reported as `unenforced`, as on macOS. netd now strips client copies of every `Tailscale-*` header and the underscore spellings `X_TW_*` and `tw_*` by prefix, not only a list of exact names. docs/security-model.md now lists what the process-tier sandbox confines on each platform and Landlock ABI, which hosts may be shared between tenants, and whom a tenant trusts (host operator, mgmt operator, tailnet admin) (#100).
