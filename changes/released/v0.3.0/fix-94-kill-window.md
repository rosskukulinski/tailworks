---
kind: security
upgrade: []
---
`tw kill` no longer returns while the killed object can still answer through
its workload. When a host was already killing the object (the heartbeat had
picked up the kill), mgmt's kill push used to return at once, before workerd
had stopped the object, so a request through the workload could still reach
it for a moment. The push now waits for that kill to finish. If workerd does
not confirm the in-process kill, the host now reports the push as failed
instead of succeeded, and the next heartbeat tries again.
