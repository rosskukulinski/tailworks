---
kind: breaking
upgrade: [config-default]
---
`tw logs <workload>` now prints one structured line per request, with filters (`--errors`, `--status`, `--trace`, …) and `--json` request records. Pass `--console` for the app's raw output, which used to be the default. tailworks also gains Prometheus `/metrics` and OTLP export of logs, metrics and traces (`tw telemetry`).

Operator action: scripts that read `tw logs` output need `--console`. tw-hostd and tw-mgmt now serve `/metrics` on port 9464 of their tailnet node by default (`-metrics-port 0` turns it off); scraping it needs a policy grant.
