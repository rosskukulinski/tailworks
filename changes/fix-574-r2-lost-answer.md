---
kind: fix
upgrade: []
---
An R2 put (or multipart part) that failed because tw-mgmt's answer to its
index write was lost (mgmt crashed or the connection dropped after the
write committed) no longer deletes its bytes. The index had the new
object, so the key read `r2_blob_missing` until it was overwritten
(#574). The bytes are now deleted only when the index write certainly
didn't happen; otherwise they stay, at worst as an orphan.
