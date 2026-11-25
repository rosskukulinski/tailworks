---
kind: fix
upgrade: []
---
hostd now reaps workerd and Node processes left running by a previous
hostd that crashed (SIGKILL, an OOM kill, a panic). Each of them runs in
a process group of its own, so it outlived the crash. A restarted hostd
then started new copies next to the old ones, which still held the
workloads' Durable Object storage open and ran their alarms. hostd
records the process groups it starts in `<data-dir>/children.json`, with
each process's start time so a reused PID is never killed. At startup it
kills any group still listed before it starts anything (#432). Hosts under
systemd were already covered by `KillMode=mixed`. This fixes `tw up` on
macOS (launchd), `tw up --separate-host` and `tw dev`.
