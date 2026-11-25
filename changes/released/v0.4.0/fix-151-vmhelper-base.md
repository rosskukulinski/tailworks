---
kind: security
upgrade: [protocol]
---
tw-vmhelper now decides for itself which microVM may become a base
snapshot. hostd marks the VM it boots for the base (`StartRequest.Base`),
and the helper refuses `snapshot-base` on every other VM (a claimed pool
VM, a VM with a Durable Object volume, a restored copy) and refuses to
attach a volume to the base VM. Before, it accepted any VM that still had
a free volume slot, which included a claimed workload without a volume.

Upgrade: the field is additive and the helper protocol stays at 2. A newer
hostd with an older helper works as before (the helper ignores the field).
An older hostd with a newer helper gets its `snapshot-base` refused, logs
"microVM base snapshot failed; booting instead of restoring", and boots
VMs cold until hostd is upgraded too. Only hosts with microVM snapshots
turned on are affected.
