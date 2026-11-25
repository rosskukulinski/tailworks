---
kind: feature
upgrade: [schema-expand, protocol]
---
`GET /v1/objects` takes `tag` (the full `tag:<name>`, matched exactly) and
`human` (the exact `actingFor`, as on `GET /v1/audit`), and `GET /v1/audit`
takes `workload`: records whose object belongs to the workload, and mgmt
records that name it in their detail (deploy, undeploy, secrets). The admin
UI's objects and audit pages pass these filters to the API instead of
filtering client-side, so the audit page no longer shows an empty page that
"hid N records", and the objects page's acting-for filter is now an exact
login instead of a substring. The objects page's status choices and the
audit page's event suggestions now come from the API spec, so they list
every object state and audit kind (`assigned`, `transferred`, and the 50-odd
audit kinds the UI was missing).

A new store table, `audit_workloads`, holds each audit record's workload
(the hashed records are unchanged); the migration fills it for existing
records. Records written by an older tw-mgmt replica during a rolling
upgrade have no workload entry, so `?workload=` doesn't find them.
