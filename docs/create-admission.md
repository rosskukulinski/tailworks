# Create admission: how a new Durable Object gets made

Every named Durable Object in tailworks is a tailnet node. Creating one mints an
auth key and joins a device, so **creation is a platform decision**, never a
side effect of tenant code reaching a binding. This note is the design for
that decision (red-team Pass 3, findings RT3-06 and RT3-07 in
`security/RED-TEAM.md`; fix F4 in `security/LEDGER.md`). It replaces the
public-only guard of ADR-029 D5 with one rule for every path.

## The rule

hostd creates an object only when its creation was **admitted**:

| Who reaches a new name | What admits it |
|---|---|
| A **tailnet request** to the Worker (or `/_tw/o/<Class>/<name>` on the workload node) | the request's **create ticket**: hostd stamps `cf.tw.ticket` on every request it forwards from the tailnet; the runtime's guarded env presents it back (`admit`) |
| A **cron run** (`scheduled()`) | the run's ticket, stamped by hostd on the dispatch |
| An **object** (facet or plain DO) reaching another object through its env | the object's own identity (`as`, attested by the platform's props); chained creation is the app's logic |
| An object's **`ctx.tailnet.spawn()`** | the parent object (attested) |
| The Worker's `ctx.tailnet.spawn()` | the request's ticket |
| A **Workflow** (`run()` and its steps, ADR-040) | nothing for a class with its own node: it reaches existing objects only. An object of a class with no node (`"node": false`, which is also what a class missing from `tailnet.objects` gets) mints nothing, so no admission applies, and a Workflow reaches it as an `AgentWorkflow` reaches its Agent ([#359](https://github.com/rosskukulinski/tailworks/issues/359)) |
| A **public (Funnel) request** | only `public.create` on the class, under `maxObjects` and the per-tenant lane (ADR-029 D5) |
| Anything else: `import { env } from "cloudflare:workers"`, an `env` cached in a global from an earlier request, the platform binding `__TW` reached directly, a cached admit on a killed name | **nothing**: hostd answers `403 create_not_admitted` |

Existing objects are reachable by any of these paths. Bypassing the guard
therefore loses the ability to mint nodes rather than gaining it, which is
the inversion that closes RT3-06 and RT3-07.

A kill ends every admission for the name (#153). hostd drops the name's
pending admission and its `public.create` record, and `admit` answers
`410 killed` for the name until it is purged (`tw down --purge`), or
until its workload is undeployed and deployed again (#364): mgmt forgets
the workload's killed objects on that deploy, and the host forgets its
tombstones once the workload is no longer assigned to it. After that, a
caller has to admit the name again. A tenant purge drops all of
the tenant's admissions. The runtime's per-isolate admit cache keeps only
"this object exists" answers, for 60 seconds, and a plain DO's kill evicts
its entry.

## The ticket

`v1.<expiry unix ms>.<trace id>.<hex HMAC-SHA256>` under the instance root
key (`MAC_KEY`, the same per-instance key that signs egress MACs). hostd
issues it (`hostd/gen.CreateTicket`) when it forwards a tailnet request or
dispatches a cron run, and verifies it (`VerifyTicket`) on `admit` and on a
Worker-level `spawn`. It never leaves the host. Validity is `TicketTTL`,
two minutes: a request's lifetime plus slack.

Tenant code can read the ticket (it is in `request.cf`, like everything
else about the request) and could save it in a global. What it buys is
bounded: for two minutes after a tailnet request, a public request handled
by the same isolate could create through the saved ticket, one name per
`admit`. The docs already name "an env saved in a global" as app-side
responsibility; this is the same residual, now with a clock on it.

## Where the pieces live

- **hostd** (`hostd/http.go`, `hostd/public.go`, `hostd/objects.go`):
  `ingressHandler` and `invokeCron` stamp the ticket; the `admit` op
  records an admission for an attested `as` or a valid ticket, else applies
  the public rules; `ensure` refuses to create a name with no admission
  (`needsAdmission`, `takeAdmissionLocked`); `spawn` from the workload
  needs the ticket; `ready` never creates. Admissions live in memory for
  `TicketTTL` and are consumed by the create.
- **Platform worker** (`hostd/gen/js/platform.js`): the Host DO awaits
  hostd's `ensure` before any tenant code runs, for `fetch` and for RPC,
  and no longer reads `request.cf` to decide whether a request is public
  (that was RT3-06: tenant code controls `cf` in-process). `TailnetApi`
  passes the ticket on `admit` and strips `actingFor`/`public` from tenant
  args. `/_tw/o/<Class>/<name>` admits with the request's ticket. Cron
  runs are dispatched to the tenant service as a fetch carrying
  `cf.tw{kind: scheduled, cron, scheduledTime, ticket}`; the runtime
  wrapper turns it into a `scheduled(controller, env, ctx)` call. (workerd's
  native `Fetcher.scheduled()` builds its own controller, so a ticket could
  not ride on it.)
- **Runtime** (`js/packages/tailnet/src/runtime.ts`, embedded as
  `hostd/gen/js/tw-rt.js`): every env handed to tenant code is a
  **guarded env**: `__TW` removed, each DO namespace wrapped so that a
  name is admitted before any call reaches it (with the ticket for a
  request, with `as` for an object). Public requests keep the stricter
  guard (no `newUniqueId`, no `get(id)` without a name, no spawn/kill). A
  plain DO whose creation was refused answers `create_not_admitted` from
  `fetch`.
- **`principal`** on `spawn` is now only `"inherit"` (default) or `"none"`;
  a name there is ignored (RT3-08 b).

## Compatibility

- `import { env } from "cloudflare:workers"` and env cached in module
  globals still work for **existing** objects and for `node: false`
  classes; they can no longer **create** node-backed objects. Create
  through the env your handler receives.
- Inside a facet, `env.X.getByName(n)` keeps working with promise
  pipelining for RPC methods; the guard settles the stub before `fetch()`
  (which never pipelined).
- The `mcp` template's `ENV = env` cache is unaffected (`TwBudget` is
  `node: false`), but it is the pattern to avoid.
- The hostd API op `ready` used to create the object if it was missing; it
  now answers `unknown_object`. `spawn` always precedes it.

## Tests

- `hostd/gen` `TestCreateTicket`; `TestGeneratedWrapperPublic`
  (`HostDORechecksWithHostd`, `TailnetAdmitsWithTicket`);
  `TestGeneratedWrapperProbe`, `TestGeneratedCron` on the fetch-dispatched
  scheduled path.
- `hostd` `TestPublicIngress` §3b: the raw binding, its RPC path, a direct
  `__TW.call("spawn")` and a cached env from a tailnet request all fail
  closed from the internet and from the tailnet; the guarded env creates;
  existing objects stay reachable.
- `js/packages/tailnet/test/public.test.js`: ticketed admits, `__TW`
  absent, a ticket-less tailnet request cannot admit a new name.
