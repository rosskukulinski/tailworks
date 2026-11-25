---
kind: fix
upgrade: []
---
Idle pre-joined pool nodes (ADR-031) now rejoin from saved state when
tailworks restarts or the host reboots. Before, they stayed offline while
mgmt still listed them as ready, so the first create of their class after a
restart registered a new node instead of using the warm one, and the old
device was left behind.
