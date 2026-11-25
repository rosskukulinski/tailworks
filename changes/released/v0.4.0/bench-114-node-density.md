---
kind: feature
upgrade: []
---
A host's default node capacity now comes from its RAM: a quarter of it at
8 MiB per object node, at least 50 and at most 1000. Before, every host
advertised 1000. That figure is twice the measured cost of a node under
request load. Per-node memory and registration rates on Linux and macOS
are published in docs/limits.md ("Per-node memory" and "Registration
rate"), and `make bench` reproduces them. docs/INSTALL.md has a sizing
section.

Operator action: a host with less than 32 GiB of RAM now advertises fewer
than 1000 nodes (an 8 GiB host 256, a 16 GiB host 512). To keep the old
figure, pass `tw-hostd -capacity-nodes 1000`.
