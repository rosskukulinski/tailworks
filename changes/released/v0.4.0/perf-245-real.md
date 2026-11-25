---
kind: fix
upgrade: []
---
After a reboot, tw is healthy again about 1 s after its service starts.
It used to take about 30 s: the host agent's first request to mgmt left
before the tailnet knew the route, and it waited out TCP's retransmits.
`systemctl --user restart tw` no longer sometimes takes 5 s extra. A
tailnet node could wait out a 5 s grace on close for a local connection
nobody was using. On a Linux VM against production control, restart to
healthy went from 8.0 s to 3.0 s (two-process layout) or 1.4 s
(single-process), and reboot to healthy went from 38 s to 7.3–7.7 s.
