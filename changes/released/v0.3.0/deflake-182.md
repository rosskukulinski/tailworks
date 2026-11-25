---
kind: fix
upgrade: []
---
A host restart no longer stalls an object for 2 minutes when control has expired the object's node key and its saved state is still marked logged in. That happens to a persistent object, or to an ephemeral one whose logout at shutdown didn't complete. hostd now detects the rejection within about 2 s and joins again under the same hostname with a fresh key. Before, the rejoin waited out the join timeout and held a join slot for the whole time.
