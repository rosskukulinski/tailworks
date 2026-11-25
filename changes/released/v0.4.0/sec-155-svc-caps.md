---
kind: security
upgrade: [config-default, cli-json]
---
App capabilities on calls through a Tailscale Service now match the policy
(H22, #155). Before, a workload (and `tw-mgmt` behind `svc:tw-mgmt`) worked
out a caller's caps as if the call had gone to the node's own address. So on
a call through the Service VIP, `caps()` was missing the caps of grants to
the `svc:` name, and it showed the caps of grants to the workload's tags even
though the caller hadn't dialed the node. Now a call through the VIP gets
exactly the caps of grants whose `dst` is the `svc:` name. A call to the
node's own name or address gets the caps of grants to its tags, as before.

`tw deploy` and `tw policy` warn (`! preflight  access:`) for each caller
whose caps are on the workload tags but not on its `svc:` name, and print the
grant to add. `tw policy` also prints the caller grant to the `svc:` name
(`serviceCallerGrant` in `--json`).

Operator action: this can take away caps that callers of a Service used to
get. If a caller of a workload behind a Service gets its app capabilities
from a grant to the workload's tags, for example

    {"src": ["tag:agent"], "dst": ["tag:tw-wl-support"], "app": {"example.com/cap/mcp": [{"tools": ["lookup_order"]}]}}

then add the same `app` to its grant to the Service, or add one:

    {"src": ["tag:agent"], "dst": ["svc:support"], "ip": ["tcp:80"], "app": {"example.com/cap/mcp": [{"tools": ["lookup_order"]}]}}

Run `tw policy` in the app's directory to list the grants to add (`tw deploy`
prints the same warnings). For HA mgmt behind `svc:tw-mgmt`, the admin cap must be on the
grant to `svc:tw-mgmt`, as `docs/services.md` already shows:

    {"src": ["autogroup:admin"], "dst": ["svc:tw-mgmt"], "ip": ["tcp:80"], "app": {"github.com/rosskukulinski/tailworks/cap/admin": [{}]}}

Without it, admins who reach mgmt through the Service are no longer admins.
Workloads that aren't behind a Service, and single-mgmt `tw up`
installs (admins reach `tag:tw-mgmt` by node name), are unchanged.
