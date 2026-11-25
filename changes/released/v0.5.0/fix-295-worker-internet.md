---
kind: feature
upgrade: [cli-json]
---
`tw up --worker-internet=off` turns off Workers' internet egress for the
whole host, as `tw-hostd -worker-internet=off` already could. The default
is `direct`: Workers' `fetch()` reaches public internet hosts through the
host's network, narrowed by each workload's `tailnet.egress.internet`. The
setting is saved in `tw.json`, so `tw down` / `tw up`, a service restart
and `tw upgrade` keep it; give the flag again to change it. `tw status`
shows it on a new `internet` line (and as `workerInternet` in
`tw status --json`), and `tw doctor` has a `worker_internet` check that
warns when the running host still runs a different setting than `tw.json`.
