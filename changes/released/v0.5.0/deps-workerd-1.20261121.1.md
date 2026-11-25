---
kind: fix
upgrade: [workerd]
---
The pinned workerd moves from 1.20261118.1 to 1.20261121.1. `fetch()` now
rejects with a `TypeError` when a response head doesn't parse
(cloudflare/workerd#8535), `Body.formData()` rejects with the body stream's
error (#8679), `new Request(request, init)` leaves the original body usable
when it throws (#8681), and `TextDecoder` keeps its state after a fatal
streaming error (#8658). In `node:net`, connect failures to unknown hosts carry
Node's `ENOTFOUND` properties (#8648) and IDN hosts are converted to ASCII
(#8709). Apps don't need changes.

Operator action: workerd moves to 1.20261121.1.
