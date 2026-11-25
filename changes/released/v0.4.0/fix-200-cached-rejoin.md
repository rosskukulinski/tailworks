---
kind: fix
upgrade: []
---
A node that the coordination server rejects after it came up is now
re-minted under the same hostname instead of staying "ready" on a dead
device. This happened after a host restart when the node had a saved
netmap cache (it comes up from the cache before control answers), and when
a running node's key expired or its device was removed. The host closes the
dead node, joins a new one with a fresh key, and reports the new node to
mgmt. A node that is being killed or stopped is not brought back.
