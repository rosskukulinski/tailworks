---
kind: feature
upgrade: []
---
The admin UI now shows logs. A workload's logs page
(`/ui/tenants/<tenant>/workloads/<workload>/logs`, linked from the
deployment, tenant and overview pages) has two tabs: **Requests** shows the
request records newest first, with each request's `console.*` lines and
exceptions, and filters by node, status, caller, trace and errors only.
**Console output** shows the workload's raw output. Each node (object) page
links to `/ui/objects/<id>/logs`, which shows that node's records. The pages
refresh every 5 s, so they follow the log, and they work for workloads with
replicas. They read the same `GET /v1/…/logs` as `tw logs`, so there is no
API change.
