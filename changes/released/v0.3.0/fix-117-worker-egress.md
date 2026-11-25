---
kind: fix
upgrade: []
---
A Worker's `fetch()` to an internet name (for example `https://api.github.com/`) is now refused with 403 `internet_egress_unavailable` and a one-line detail that links the gateway recipe (docs/recipes.md "Call an internet API"), instead of `peer_not_visible` with a hint about grants that could never help. A tailnet name that isn't in the netmap (a bare short name, a name under a tailnet's MagicDNS suffix, or any `*.ts.net` name) is still `peer_not_visible`. Nothing that was refused is now allowed. The README and docs now say plainly that Workers have no internet egress and that no setting turns it on; `tailnet.egress.internet` applies to containers only. Code that matched `peer_not_visible` for internet URLs should match `internet_egress_unavailable` as well.
