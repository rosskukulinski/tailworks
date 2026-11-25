---
kind: fix
upgrade: [workerd]
---
The pinned workerd moves from 1.20261114.1 to 1.20261118.1. That fixes two
memory-safety issues in workerd's Rust JSG bindings (cloudflare/workerd#8401,
#8403) and several JSG garbage-collection issues, and fixes nested dynamic
imports (#8517, #8541). A `DigestStream` now stays alive while its sink runs
(#8533), and `CompressionStream` and `DecompressionStream` gain `"brotli"`
(#8483). Apps don't need changes.

Operator action: workerd moves to 1.20261118.1.
