---
kind: fix
upgrade: [workerd]
---
The pinned workerd moves from 1.20261114.1 to 1.20261115.1, which fixes
two memory-safety issues in workerd's Rust JSG bindings
(cloudflare/workerd#8401, #8403). Apps don't need changes.

Operator action: workerd moves to 1.20261115.1.
