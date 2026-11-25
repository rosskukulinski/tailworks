---
kind: fix
upgrade: []
---
A host that self-fences (it lost its heartbeat to mgmt) now stops claiming
queue messages at once. Before, a consumer's long poll that was already
waiting could still claim a message sent in the first half second or so
after the fence. The host handed it back, since its workload was stopped,
but a fenced host must not claim anything.
