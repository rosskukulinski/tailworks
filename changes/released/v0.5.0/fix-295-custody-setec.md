---
kind: feature
upgrade: [cli-json]
---
`tw custody migrate --to setec --setec-url <url>` moves an install that
started on file custody to setec, so it can run several mgmt replicas. Run
it with tailworks stopped. It maps every file custody entry back to its
name, and refuses (`custody_unmapped`) if any entry can't be named. To name
entries written before mgmt indexed custody names, it first backfills
`custody_names` from the store's rows and the audit trail. It copies every
entry, reads each one back from setec, backs up the state, and then
switches `tw.json` to setec custody. File custody is left as it was. See
`docs/postgres.md`, "Move file custody to setec".
