---
kind: feature
upgrade: []
---
`tw backup` now also copies each workload's Durable Object storage and,
with the `fs` backend, R2 object bytes, while tailworks runs. Before, you
had to `tw down` to back these up. Each object's SQLite database is copied
with `VACUUM INTO` through a read-only connection that shares workerd's
locks, so each copy is the object as of one committed write and workerd
keeps serving. R2 blobs are immutable, so they are copied (or hard-linked
on the same disk) before and after mgmt's store. `backup.json` has a new
`hostd` entry that lists the workloads and counts the databases and blobs.
`tw restore` puts them back and moves what it replaces into the safety
backup. `tw backup --hostd=false` copies the control plane only, as
before. microVM DO volumes, Node.js workloads' data directories and other
hosts' storage are still not included; `backup.json` says so.
