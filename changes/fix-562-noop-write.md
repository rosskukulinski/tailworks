---
kind: fix
upgrade: []
---
A storage write that changes nothing on the host's copy (an R2 or KV
delete of a key the copy doesn't have, an R2 put whose `onlyIf` fails,
a D1 `UPDATE` that matches no row) is now checked against tw-mgmt's
journal before it is acknowledged. A workload's replica whose copy was
behind could acknowledge a delete of a key another replica had just
written, and the key stayed (#562). While tw-mgmt is unreachable such a
write now fails like every other write, instead of being acknowledged
unchecked. It costs one journal read.
