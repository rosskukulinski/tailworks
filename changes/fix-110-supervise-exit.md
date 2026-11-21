---
kind: fix
upgrade: []
---
The supervisor (`tw __supervise`) now exits 1 when mgmt or the host stops
on its own: mgmt's listener failed after start (single process), or a
tw-mgmt or tw-hostd child kept exiting and the supervisor gave up (two
processes). It used to exit 0, which systemd (`Restart=on-failure`) and
launchd (`KeepAlive` on an unsuccessful exit) treat as a clean stop, so a
`tw up --service` install stayed down. A clean stop (`tw down`,
`systemctl --user stop`) still exits 0. The exit error (`mgmt_failed` or
`host_failed`) is in `tw logs --component supervisor`.
