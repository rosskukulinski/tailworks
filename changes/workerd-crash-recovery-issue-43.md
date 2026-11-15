---
kind: fix
upgrade: [protocol]
---
A workerd that crashes after its deployment has served is now restarted until it comes back. hostd retries with exponential backoff (200 ms doubling to 5 min) and never gives up. Before, six quick exits marked the deployment `failed` for good. Before each restart hostd kills the old workerd's process group and waits for it to empty. The stale ingress socket is then removed, so a restart no longer fails with `bind(): Address already in use`. hostd also no longer misses an exit when a child process still holds workerd's log. From the second exit in a row the deployment reports a new status, `crashloop`, and its error gives the attempt count, the next delay and the last log lines. `twd ls`, `--json`, the console and the API show this status. The deployment goes back to `active` by itself once workerd answers again. `twd deploy` stops waiting with `deploy_crashloop` when the deployment enters a crash loop. A deploy whose workerd never becomes healthy (a bad bundle or config) still ends `failed` within a few seconds.

Operator action: clients that match on deployment status should treat `crashloop` as "assigned here, not serving right now". A deployment left `failed` by this bug on an older host needs one `twd deploy` after the upgrade.
