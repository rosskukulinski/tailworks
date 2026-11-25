---
kind: fix
upgrade: []
---
`tw audit` and audit verification no longer report an intact chain as
broken while records are being appended (#93). Verification read a chain's
records and its head in two separate queries, so a record appended between
them made the head look one record ahead and the chain failed with "chain
head mismatch" (`audit_unverified`). Both are now read in one read
transaction. Detection of real truncation and tampering is unchanged.
