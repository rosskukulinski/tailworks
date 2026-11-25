---
kind: fix
upgrade: []
---
Pool nodes no longer leak when a workload moves to another host (#567).
A pre-joined pool node lives on the host that warmed it. When the
workload's lease moved (`tw deploy --replace`, or `--force-takeover` after
a host was cut off or stalled), the old host's pool nodes stayed `ready` in
mgmt with no host to run them, and their devices stayed on the tailnet.
Each move left up to `--pool-nodes` devices per hot class behind. Now a
deploy that places the workload on a host kills the pool nodes another
host warmed for it, which deletes their devices.
