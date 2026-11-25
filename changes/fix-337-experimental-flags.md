---
kind: security
upgrade: [config-default, feature-string, protocol]
---
A deployment whose `compatibility_flags` list a flag workerd marks
experimental (`unsafe_module`, `rtti_api`, `precise_timers`, …) is now
refused with `403 experimental_flag` (red-team H-compat-flags, #337).
Every workerd tailworks starts runs with `--experimental` for its own
platform worker, and a tenant's flags reached it unfiltered, so those
flags, and every experimental flag a workerd release adds, were
tenant-selectable: `unsafe_module` gave tenant code `workerd:unsafe` and
string eval in its own isolate. Nothing left the tenant's own process.
mgmt refuses the deploy and its dry run (`tw deploy`'s preflight) from
the known list of the pinned workerd's experimental flags, and a host
refuses the generated config from that list and from the pinned workerd
itself, so a flag a future bump brings is caught too. Stable flags
(`nodejs_compat` among them) are unaffected.

Operator action: none unless an app of yours sets an experimental flag:
it fails to deploy with `experimental_flag` until the flag is removed. To
keep running such apps, start the host with `tw up
--allow-experimental-flags` (or `tw-hostd -allow-experimental-flags`); the
host then advertises the new `experimental-flags` feature and mgmt places
those deployments only on hosts that have it.
