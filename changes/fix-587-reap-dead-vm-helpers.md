---
kind: fix
upgrade: []
---
On Linux, a hostd that runs microVMs as root without the `tw-vmhelper`
service no longer leaves a crashed predecessor's VMs running (#587). Its
in-process helper names its VMs, network namespaces, jails and cgroups
after hostd's pid. After a crash (an OOM kill, a panic, SIGKILL), the
restarted hostd has a new pid and never cleaned up the old ones: every
Firecracker VM kept running, still running its object's code, and its
netns, jail and cgroup stayed behind. Now hostd removes the state of every
in-process helper whose hostd is gone when it starts, and its own when it
stops. Hosts set up with `sudo tw host setup --microvm` (the helper
service) were not affected.
