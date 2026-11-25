# fix-facet-host-rpc

Follow-up to #211 (PR #213), which left one open question: can tenant code call
a facet Host's helper methods over RPC? Ledger row H27.

## Finding

Yes. The tenant Worker's raw binding (`import { env } from "cloudflare:workers"`)
is the platform's `Host_<Class>` namespace, and a facet's `env.X` (NsProxy)
returns the same Host stubs. workerd exposes a DO's prototype methods over RPC,
so on master tenant code could call:

- `__tw_intercept`: starts the facet and runs tenant code for a name hostd
  never admitted (the RT3-06 admission check is skipped).
- `__tw_alarm_op`: sets an alarm on any name. The Host's `alarm()` then runs
  the facet without admission.
- `__tw_kill`: kills an object without hostd. There is no audit record, and
  mgmt still shows the object live while it answers 410.
- `__admitted`, `__ensure`: reachable. hostd refuses an unadmitted name, so
  through the raw binding these did nothing worse.
- `__facet`: reachable, but workerd refuses to serialize a facet stub, so the
  facet could not be handed out.

Own properties (`__killed`, `__actingFor`, `env`, `ctx`) are not exposed over
RPC, and `alarm` is reserved.

## Fix (`hostd/gen/js/platform.js`)

- `__admitted`, `__ensure` and `__facet` become module functions
  (`hostAdmitted`, `hostEnsure`, `hostFacet`) and are no longer methods.
- `__tw_kill`, `__tw_intercept` and `__tw_alarm_op` take a trailing `cap` and
  refuse anything but `hostCap(env)`: an HMAC of a constant under `MAC_KEY`,
  which only the platform worker holds. Their platform callers (admin kill,
  the intercept route, TailnetApi alarm delegation) pass it. The plain-DO
  wrapper's `__tw_kill` and `__tw_intercept` ignore the extra argument.

## Other wrapper classes checked

The platform entrypoints tenant code can reach (`TailnetApi`, `NsProxy`,
`WorkflowGate`, `QueueFacetProxy`, the store facets and backends, `Egress`,
`WorkloadEgress`) expose only their intended methods, and their helpers are
`#private`. Store DOs are not bound into tenant code.

## Tests

- `go test -run TestHostd/FacetHostInternalsNotRPC ./hostd/`: failed on
  master's platform.js. Every call answered 200 through the raw binding and
  from a facet, and the tenant's `__tw_kill` left `hrok` answering 410. Passes
  with the fix.
- `go test -run TestHostd ./hostd/`: ok.
- `go test -run 'TestContainerEgress$|TestContainerPolyfill$' ./hostd/`
  (intercept path): ok.
- `make lint`: ok. `make changes-check`: ok.
- `make test`, `make test-sdk` (mbp runner): see the PR.

## Gaps

- The plain-DO wrapper's `__tw_kill` and `__tw_intercept` (runtime.ts) are
  still reachable from tenant code. A plain DO runs in the tenant isolate, and
  the tenant's own class can write its kill marker, so this is not a boundary.
  It is left as is to stay out of #148's runtime.ts work.
