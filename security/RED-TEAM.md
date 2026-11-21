# tailworks Red-Team Findings

Date: 2026-11-08 · Branch `rt-redteam`. Companion: `THREAT-MODEL.md`, `attacks/`.

Method: adversarial code review of M0–M9 (`STATUS.md`) against the design threat model,
plus hermetic PoCs. Where a finding is grounded in code I cite file:line; where I executed
it I say so. Live tailnet / microVM exploitation was **not** run: this red-team host is
macOS, and the brief forbids touching the other engineers' worktrees, Lima VMs, and the
in-use test tailnets beyond my own `ttest-rt-*` prefix. The code-level evidence is
reproducible and one High (RT-01) ships an executable failing test.

Ranking: Critical = cross-tenant / full platform compromise; High = cross-trust-boundary
within a tenant or defeat of a headline security control; Medium/Low as noted.

| ID | Sev | Claim attacked | STATUS accurate? |
|----|-----|----------------|------------------|
| RT-01 | **High** | DoD(d) spawn "narrower caps"; RFC §4.6 least-privilege delegation | Honest gap, but DoD/e2e headline overstates |
| RT-02 | **High** | "audit … chain verified" as a forensic record | Honest about chain, silent on content authenticity |
| RT-03 | **Critical** (Linux process/Node) | E2E-MULTI "isolation holds"; DoD item 2 | Honest gaps, but headline is macOS-only |
| RT-08 | **High** (Node tier) | "egress leaves from the object's node" / ADR-019 | Honest (M9 gap), but not in the isolation headline |
| RT-04 | Medium | per-object attribution within a workload | Honest (M8 gap) |
| RT-05 | Medium | egress MAC integrity | Honest (M3 gap) |
| RT-06 | Low/Med | `--policy-apply` cannot widen tenant policy | Partially — widening is possible by design |
| RT-07 | Low/Med | "kill in under 1 s"; killed object stops | Honest, well-measured |

---

## RT-01 (High) — Spawn `lim` is never bound to the child's identity

**Claim:** DoD item 1(d): `spawn()` creates a sub-agent "with its own node and **narrower
caps**"; RFC §4.6 delegation (`caps ⊆ parent`, least privilege). STATUS M4(d): child caps
"narrower than the parent's … checked by mgmt".

**Reality:** mgmt validates the requested `lim` (`SpawnOptions.caps`) against
`effective(parent)` in `Policy.CheckSpawnCaps` (`mgmt/policy.go:91`) — but then creates the
child object tagged with **`class.Tags`**, not a subset derived from `lim`:

```
// mgmt/service.go  (CreateObject)
o := Object{... Tags: class.Tags, Caps: r.Caps, ...}   // Caps(lim) stored, Tags = class tags
...
if len(o.Caps) > 0 { detail["lim"] = o.Caps }          // lim only recorded in audit
```

The child's node joins tagged `class.Tags`. A receiver authorizes on the WhoIs capMap,
built straight from the tailnet policy grants for those tags
(`netd/ingress.go:CallerFromWhoIs` → `w.CapMap`). **`lim` never enters the node identity or
the receiver's capMap.** `effective(child.Tags)` can — and is designed to — exceed `lim`.
And because tag ownership (`TagOwnedBy`) only checks *ownership*, not cap-subset, the child's
class tag can be granted MORE than the parent's tags: the child escalates past both `lim`
and the parent.

**Exploit:** operator defines one sub-agent class `tag:sub` granted `{mcp:[lookup_order,
refund]}`, intending to hand each spawn a narrow `lim`. A prompt-injected parent calls
`spawn(Sub, {name, caps:{mcp:[lookup_order]}})` — passes `CheckSpawnCaps` (lim ⊆ parent) —
but the deployed child presents `tag:sub`'s full `{lookup_order, refund}` to any MCP server
it calls. `lim` is cosmetic.

**Evidence (executed):** `attacks/rt01_spawn_lim_test.go`. With the finding-ID skip
removed it FAILS against current code:

```
--- FAIL: TestRT01_SpawnLimBindsToIdentity
    RT-01: child spawned with lim=[lookup_order] still carries `refund` via its class tag
```

**Impact:** the core least-privilege control for a compromised agent (`lim`) provides no
boundary; delegation is coarse (tag granularity) and can escalate. Defeats the DoD(d)
security intent.

**Fix:** intersect the node's effective caps with `lim` at the receiver — mint the child
with a node attribute / assertion carrying `CapMap ∩ lim` and have netd/the platform
enforce it (RFC §4.6 node cert), or derive per-spawn narrowed tags. Until then, stop
advertising `lim` as a security boundary.

**STATUS accuracy:** M3 known gap already says "Spawn caps are not enforced on the
receiving side … not yet written into the node cert (CapMap ∩ lim)" — **accurate and
honest**. But DoD(d) and the M4/M6 e2e "narrower caps" lines read as if enforced; they
should be qualified.

---

## RT-02 (High) — Tenant code forges audit content (fake trace IDs, fake "allow")

**Claim:** `tw audit` shows the human→agent→sub-agent chain and "chain verified"; audit is
the forensic record of what a (possibly compromised) agent did.

**Reality:** the host API `audit` op (`hostd/http.go`, case `"audit"`) forces `ObjectID` to
the attested caller (good) but, for kinds `allow|deny|egress`, copies attacker-controlled
fields verbatim into the **same hash chain** as platform security records:

```
ar.Grant, _  = rec["grant"].(string)
ar.Code, _   = rec["code"].(string)
ar.ActingFor,_= rec["actingFor"].(string)
if tid,_ := rec["traceID"].(string); tid != "" { ar.TraceID = tid }   // override real trace
```

Reachable from tenant code via `ctx.tailnet.audit([...])` (`js/.../runtime.ts:115`).

**Exploit:** a prompt-injected agent emits
`{kind:"allow", grant:"example.com/cap/mcp: refund", actingFor:"ceo@victim",
traceID:"<a human's trace>", detail:"wired $1M"}`. The record chains validly
(`chain verified` stays true), is **indistinguishable** from a platform authorization
decision in `tw audit`, is cross-linked into a victim principal's trace via the overridden
`traceID`, and frames a human via `actingFor`. It can also bury real `deny`/`egress`
evidence under noise.

**Evidence:** `attacks/rt02_audit_forgery_test.go` (skip-marked RT-02) reproduces the
handler's field-copy and encodes the invariant: app records must be provenance-namespaced
and must not override the request trace.

**Impact:** audit cannot be trusted as the forensic account of a hostile agent — exactly
its purpose. Tamper-evidence of the chain is intact; **authenticity of content is not.**

**Fix:** namespace app-submitted records (`app.*` kind or a `source:"app"` field the UI
never conflates with platform records); ignore caller-supplied `traceID`/`actingFor` (bind
to the request's real trace and verified principal); constrain `grant`/`code` to app-origin
values. 

**STATUS accuracy:** STATUS claims chain verification only (true) and lists app records as
a feature; it does not claim content authenticity. The gap is unstated — worth adding.

---

## RT-03 (Critical, Linux process/Node tiers) — Tenant → full platform takeover via mgmt admin socket

**Claim:** E2E-MULTI "isolation holds: a tenant-A object can't see or reach tenant B"
(DoD item 2). STATUS M7: "The Seatbelt profile closes all of these on macOS."

**Reality:** the mgmt admin unix socket authorizes **only** by same-uid peercred
(`mgmt/server.go:ListenUnix` + `uidListener.Accept`), and all tailworks components plus every
tenant workload run as one uid. On Linux the process and Node tiers apply **no** filesystem
sandbox (`hostd/seatbelt.go` is macOS-only; `ResolveSandbox→none` elsewhere; no
Landlock/seccomp is wired in). So a tenant process that can issue a raw AF_UNIX connect
reaches mgmt as the `local` admin principal: kill any tenant's objects, `GET /v0/tenants`
(enumerate every tenant + their `apiKeyFile` paths), then read each tenant's node keys from
`~/.tailworks` state dirs and decrypt custody (`custody.key` is 0600 but same-uid readable).

- **Node tier on Linux:** tenant code is ordinary Node — **no exploit needed at all**
  (STATUS M9: "a malicious Node app can bypass the proxy (raw sockets)").
- **workerd process tier on Linux:** needs a V8 isolate escape first, then identical.

**Evidence:** `attacks/rt03_node_mgmt_socket_takeover.md` (PoC) + `rt03_mgmt_socket_test.go`
(skip-marked RT-03: dials the real `ListenUnix` listener same-uid and asserts it must be
refused without a credential — it currently is admitted). Not run against a live Linux
hostd (none here; VMs off-limits).

**Impact:** complete cross-tenant compromise from unprivileged tenant code on multi-tenant
Linux process/Node hosts. Breaks DoD item 2 off macOS.

**Fix:** don't gate admin on same-uid peercred — per-tenant uid + fs namespace, or a
per-component bearer token on the socket, or restrict multi-tenant Linux to the microVM
tier. Document process/Node Linux tiers as single-tenant until then.

**STATUS accuracy:** STATUS is candid ("0700 is not isolation"; M7 "a hostd compromise
exposes every tenant's node keys"; integration "the Node tier on Linux has no per-tenant
confinement"). But the **E2E-MULTI isolation headline is macOS-specific** and should say so
loudly, since the docs present Linux as first-class (docs/install-testing.md, "Linux requirements").

---

## RT-08 (High, Node tier) — Egress containment is advisory; raw sockets bypass ADR-019

**Claim:** objects' `fetch()` leaves from the object's node and is bounded by the ADR-019
egress guard (no loopback/LAN/metadata/internet).

**Reality:** for the Node runtime, egress enforcement is an **environment HTTP proxy**
(`HTTP(S)_PROXY`, `js/packages/node`). A hostile Node app opens raw TCP/UDP sockets or
installs its own `fetch` dispatcher and reaches loopback, the host LAN, link-local
`169.254.169.254`, and the internet directly — the ADR-019 guard is never consulted. The
microVM gVisor gateway would make this real, but Node is **rejected** in the microVM tier
(`runtime:node + tier:microvm` unsupported), so on every platform the Node tier's egress is
unenforced.

**Evidence:** STATUS M9 gap states it verbatim ("a malicious Node app can bypass the proxy
(raw sockets, its own dispatcher) and reach loopback, LAN and internet"). Grounded in
`js/packages/node` (proxy-via-env only) + `hostd/node.go`. SSRF to cloud metadata / host
services is the practical impact.

**Impact:** SSRF and egress-containment bypass for any Node workload; combined with RT-03 on
Linux it is also the delivery vehicle for the socket takeover.

**Fix:** enforce egress out-of-process (per-tenant netns + transparent proxy / microVM) for
the Node tier, or restrict it to single-tenant trusted apps. Don't present env-proxy as a
security boundary.

**STATUS accuracy:** honest in the M9 gap list; just not reflected in the top-line egress
claims.

---

## RT-04 (Medium) — One MAC key per VM lets a guest compromise forge attribution among its objects

The microVM tier ships `MAC_KEY` in the guest config (`env.MAC_KEY`, readable in-guest by
design). All objects in one VM share it, and `isolation:"object"` placement is not built.
A single V8 escape inside the guest forges the egress MAC for any object in that VM,
impersonating a sibling object's identity on egress (including a higher-privileged parent in
the same workload — compounds RT-01). Scope: one tenant's workload. STATUS M8 states this
("a V8 escape could impersonate the other objects in the same VM"). Fix: per-object MAC keys
or `isolation:"object"` VM placement.

## RT-05 (Medium) — Egress MAC has no replay/nonce cache and is not bound to the destination node

`gen/mac.go` verifies a 2-minute-skew HMAC over `class\nname\nmethod\nurl\nts\nnonce` with
**no nonce cache**. In the workerd tiers the key isn't tenant-visible, so this is latent;
in the microVM tier (RT-04, key readable) a captured or recomputed MAC can be replayed
within 2 minutes, and the MAC does not bind to *which* object node must originate the
request, only to the class/name claim. Fix: nonce cache + tighter skew, and bind the MAC to
the resolved egress node.

## RT-06 (Low/Medium) — `tw deploy --policy-apply` can widen the tenant tailnet from a bundle's tailworks.jsonc

`internal/policy.Merge/Apply` only *adds* tagOwners/grants, validates, and writes with
If-Match (sound against clobbering). But the additions are derived from the deploy's
`tailworks.jsonc`/preflight. A hostile or mistaken bundle run with `--policy-apply` injects new
`grants`/`tagOwners` into the live tenant policy — a privilege-widening supply-chain edge.
Mitigated by the explicit flag + validation; the operator should diff the printed snippet
before applying. Fix: show a destructive-change diff and require per-grant confirmation;
never let a bundle add `tagOwners`.

## RT-07 (Low/Medium) — Kill retraction is slower than the "<1 s" headline for netmap removal

In-flight flows RST in <1 ms (strong). But netmap removal on production control is bimodal
to **3–5 s**, and a *new direct* dial to a killed node's 100.x IP black-holes to the
caller's timeout (~5 s) rather than failing fast — a brief window where a killed object's
node is still joinable/addressable and clients hang. STATUS measures and discloses this
(M1 findings 2–3, M3/M8). Not "keeps serving" (the tombstone RSTs in-netmap peers), but the
"kill under 1 s" headline holds only for callers with an active flow. Fix: delay device
delete by ~the tombstone window, and have clients treat netmap-absence as dead.

---

## Regression tests

- `attacks/rt01_spawn_lim_test.go` — **executed**, fails without the skip.
- `attacks/rt02_audit_forgery_test.go` — skip-marked RT-02, encodes the invariant.
- `attacks/rt03_mgmt_socket_test.go` — skip-marked RT-03, dials the real listener.

All compile and `go vet` clean; skip-marked so CI stays green until the fixes land.

---

## Fix status (branch `rt-fixes`, 2026-11-08)

| ID | Status | Where |
|----|--------|-------|
| RT-01 | **Fixed**: node certificates (ADR-010) signed by mgmt at ready; receiving netd computes `CapMap ∩ lim`, fails closed | STATUS.md "RT fixes"; `rt01_spawn_lim_test.go` un-skipped |
| RT-02 | **Fixed**: tenant code produces only `tenant_note`/`source: tenant`; mgmt and `tw audit` enforce/show provenance | `rt02_audit_forgery_test.go` un-skipped |
| RT-03 | **Fixed**: per-host bearer token on the unix socket, rotated on `tw up`; peercred kept as an extra check | `rt03_mgmt_socket_test.go` un-skipped |
| RT-04 | **Partial**: per-object derived MAC keys; the root still lives in the guest, so `isolation: "object"` remains the real fix | STATUS.md |
| RT-05 | **Fixed** for what exists: nonce caches on egress MACs and the Node caller header; OBO assertions are still unimplemented | STATUS.md |
| RT-06 | **Fixed**: `--policy-apply` only adds `tag:tw*` tagOwners and tw-sourced grants, refuses any non-additive diff | `internal/policy` hostile table + fuzz tests |
| RT-07 | **Documented**: kill tier 1 (RST + tombstone) is the fast path; netmap retraction stays a control-plane property | STATUS.md |
| RT-08 | **Fixed/partial**: `@tailworks/node` `enforceEgress()` (cooperative); hostd reports `egress unenforced`, refuses Node-tier on multi-tenant hosts; docs say only Landlock/microVM is a boundary | `docs/node-tier.md` |

---

# Pass 2 (live) — 2026-11-08, branch `rt2-live`

Pass 1 worked from code on macOS and ran no live attacks. Pass 2 attacked the
**merged** system on real Linux. Environment: a dedicated Lima VM
`twl-rt2-u24` (Ubuntu 24.04 arm64, kernel 6.8, **Landlock ABI 4**, vz, nested
virt, mounts nothing), the real `linux-arm64` release built on the Mac
(`make release`) and installed with `install.sh` as a user would. No personal
tailnet, no system tailscaled, no policy writes; `tw-dev` / `twl-*` / the FV
worktree untouched. The microVM tier could **not** be exercised live: a
Mac-built release carries no microVM guest (`make vm-assets` is Linux-only and
needs the Firecracker toolchain + guest image; `tw doctor` confirms
"this release has no microVM guest … process tier only"). Those verdicts are
code-grounded and labelled so.

Evidence lives in `security/attacks/`: the pre-existing regression tests
(`rt01`/`rt02`/`rt03`), plus Pass-2 additions `rt01_cert_attacks_test.go`,
`rt05_replay_test.go`, `rt06_policy_json_tricks_test.go`,
`rt_sandbox_linux_test.go` (the Linux escape battery), and the live script
`rt03_live_mgmt_socket.sh`. The whole `./security/attacks` suite was
**cross-compiled to linux/arm64 and run inside `twl-rt2-u24`**: PASS (the
`//go:build linux` battery only means anything there).

## Verdicts

| ID | Verdict | Live evidence |
|----|---------|---------------|
| RT-01 | **HOLDS** | node cert (`internal/nodecert`) signs `lim`; receiver computes `CapMap ∩ lim` (`netd/ingress.go:applyCert` → `internal/caps.Intersect`). Live: a child spawned `lim=[lookup_order]` loses `refund`; a cert for node `nA` is rejected for a re-minted `nB` (stale-cert reuse), an **expired** and a **future-iat** cert are rejected, a forged/other-key cert fails signature, an unparseable/omitting `lim` **fails closed** (empty caps), and a failed cert *fetch* withholds all caps (`netd/ingress.go:76`, `CapsWithheld`). `rt01_*_test.go` PASS on macOS and in the VM. |
| RT-02 | **HOLDS** | tenant code yields only `tenant_note`/`source:tenant`; `traceID`/`actingFor` are bound to the real request, platform fields dropped, the claim preserved only inside `detail` (`hostd.TenantAuditRecord`). mgmt's ingest (`mgmt.checkAuditSource`) refuses `tenant`+non-note, a `tenant_note` without `source:tenant`, and anything claiming `source:mgmt`. Both raw-POST shapes covered; `rt02_*` PASS live. |
| RT-03 | **HOLDS (live, real binary)** | Ran the real `tw-mgmt` on its unix socket (SQLite store, no tailnet) and attacked it as a tenant would (`rt03_live_mgmt_socket.sh`): bare same-uid connect → **401 host_token_required**; forged 64-char token → **403 host_token_invalid**; pre-fix `Tw-Host-ID` alone → **401**; even `/v0/healthz` → **401**; the 0600 token file's value → **200**. Socket is `srw-------` in a `drwx------` dir; token file `-rw-------`. On a Landlock host the socket is **doubly** out of reach: the battery shows AF_UNIX connects are refused (EPERM, seccomp) and `~/.tailworks` is unreadable (EACCES), so a workload can neither open the socket nor read the token. |
| RT-04 | **PARTIAL (by design; not exploited live)** | Per-object MAC keys are derived (`gen.ObjectMACKey`); a MAC under another object's key does not verify (`rt05_replay_test.go`). The root key still lives in the guest, so a single in-guest V8 escape can still forge siblings in the *same* VM — `isolation:"object"` placement remains the real fix. Not reachable without the microVM tier. |
| RT-05 | **HOLDS** | `gen.VerifyEgressOnce` + `gen.NonceCache`: a replayed `(class,name,nonce)` inside the skew window is refused, a fresh nonce is accepted, an out-of-skew tuple is refused even with a valid MAC, and a MAC under another object's key fails. `rt05_replay_test.go` PASS live. Residual (documented): the MAC is not bound to *which* egress node originates, latent while the key isn't tenant-visible (workerd tiers). |
| RT-06 | **HOLDS** | `internal/policy.Check`/`Merge`/`VerifyAdditive` refuse `src:*`, `autogroup:*`, groups, users, non-tw `src` tags, `dst:*`, non-tw `tagOwners`, extra grant fields, and any non-additive result (the base hostile table). Pass-2 adds: a **Unicode-homoglyph** tag (`tag:twԁ-…`) is refused, and a live policy with **duplicate `grants`/`tagOwners` keys** either is refused or stays verifiably additive. See RT2-L1 below for one Low gap. |
| RT-07 | **HOLDS (documented)** | Not re-measured live this pass. Kill tier 1 (RST + 10 s tombstone) is the fast path; netmap retraction stays a bimodal control-plane property, as STATUS/pass-1 disclose. |
| RT-08 | **HOLDS on Landlock ABI ≥ 4; cooperative elsewhere (as documented)** | The **live escape battery** (`rt_sandbox_linux_test.go`, run under tailworks' real tenant profile in the VM) refuses, before any effect: read/list/write outside the instance dir (EACCES), TCP to `1.1.1.1:443`, cloud metadata `169.254.169.254:80`, and another tenant's loopback port (EACCES), `bind` (EACCES), UDP/DNS (EPERM), AF_UNIX to a mgmt-shaped socket and to `tailscaled.sock` (EPERM), `socketpair(AF_UNIX,DGRAM)` (EPERM), `ptrace`/`process_vm_readv`/`kill`/`tgkill`/`kill(-1)` against another process (EPERM). Only the egress-proxy port and the instance dir are reachable. `tw doctor`'s own live Landlock probe agrees. `execve(/bin/cat)` is **allowed by design** (`systemRO` maps `/bin,/usr,/lib` read+execute so runtimes work) and is not an escape: the child inherits Landlock under `no_new_privs` (setuid bits ignored) + the seccomp filter and is confined identically. On macOS (Seatbelt, no TCP confinement) and Linux ABI < 4, egress is cooperative — `tw doctor` warns and `tw status` says `egress unenforced` (before #100 it said `enforced` on ABI < 4); hostd still admits a second tenant there (it refuses one only on a host with no sandbox, without `--insecure-shared-host`). |

## New findings

**RT2-L1 (Low) — `policy.Check` accepts a whitespace-/control-padded tailworks tag.**
`okTag` is `strings.HasPrefix(tag, "tag:tw")` with no boundary or
whitespace check, so a `src`/`tagOwners` entry like `"tag:tw-wl-x "`
(trailing space, tab or newline) passes `Check`. *Impact is bounded:* the
accepted string is still inside tailworks' own `tag:tw*` namespace, a grant to a
space-padded tag matches no real device, and every policy write is validated
by Tailscale before apply (docs/install-testing.md, safety rule 2). So this is input-validation
leniency, not a cross-principal widening. *Fix:* require a boundary after the
prefix (exact `tag:tw` or `tag:tw-…`) and reject tags containing
whitespace/control characters. Regression: `TestRT2L1_CheckAcceptsWhitespacePaddedTag`
(skip-marked). No Critical/High/Medium new findings.

## Residual risk by tier

- **macOS process (Seatbelt).** Files and unix sockets are confined (so the
  RT-03 socket/token are unreachable), but Seatbelt does **not** confine TCP:
  the Node tier's egress is cooperative (`@tailworks/node enforceEgress()` is
  in-process defence only), so a hostile Node app can SSRF to loopback/LAN/
  metadata/internet. workerd-tier egress is still platform-enforced. Documented;
  treat multi-tenant macOS Node as trusted-only.
- **Linux process / Node (Landlock ABI ≥ 4).** Strong boundary — the live
  battery is clean, RT-03 is double-locked. Residuals: (a) ABI < 4 kernels
  can't restrict TCP, so Node egress is cooperative there (hostd warns,
  `node_egress`); (b) a user in a container-engine group or able to reach
  `tailscaled.sock` as a unix-listening Node app is root-equivalent for hostd
  itself — hostd refuses a second tenant unless `--insecure-shared-host`
  (`tw doctor host_sockets`). RT2-L1 above.
- **Linux microVM (Firecracker).** **Not exploited live this pass** (release
  without guest assets). Code review of the privileged paths found the
  expected controls intact: the vmhelper authorises each connection by
  **SO_PEERCRED uid** against `allowedUIDs`; its config must be root-owned and
  not group/world-writable; a volume image must be **owned by the caller uid**
  (no attaching another uid's image); a `(device,inode)` **busy map** blocks
  double-attach; volumes are **LUKS2** and the attach is **epoch-fenced**,
  persisted, checked before and after `cryptsetup open`, so a taken-over
  hostd's **stale-epoch** attach is refused (`CodeVolumeFenced`); a wrong key
  gives `CodeVolumeKey`. `tw host setup --undo` reverses the recorded manifest
  (unit, root-owned binaries, sysctl/hugepages, only the kvm-group members it
  added, `/etc/tailworks`), leaving each user's DO volumes. RT-04 (shared in-guest
  root MAC key) is the open gap. These should be re-verified with a live
  Firecracker run once a microVM-asset release is available.

## Supply chain / install (live)

The `linux-arm64` tarball's outer sha256 matched `dist/SHA256SUMS`; its
internal `SHA256SUMS` verified clean (12881/12881 files, no mismatch).
`install.sh` is no-sudo and confined to the prefix; `tw doctor` reports the
sandbox, host-socket and microVM posture honestly. (`workerd`'s mtime is a
fixed 1985 epoch — cosmetic.)

---

# Pass 3 (code review) — 2026-11-14, master `4f23a96`

Pass 1 and 2 attacked M0–M9. Everything added since (public ingress over
Funnel, ADR-029; Tailscale Services; cron triggers; smart placement; the
REST `/v1` API over tsnet with multi-replica mgmt on Postgres; warm auth
keys and the pre-joined node pool, ADR-031; the microVM warm pool and
snapshot restore, ADR-030) had never been attacked. Pass 3 is a
code-level adversarial review of those surfaces by four parallel
reviewers (public ingress; mgmt authorization and tenant isolation; host,
VM tier and sandbox; tenant JS versus the platform wrapper, plus supply
chain). Each claim below was re-read at the cited lines by the lead;
"executed" means a throwaway test was run (in `/tmp`, never in the
worktree). No live tailnet, VM or socket outside the repo was touched.

Ranking as before: Critical = cross-tenant / platform compromise; High =
cross-trust-boundary within a tenant, or defeat of a headline security
control; Medium/Low as noted.

## Summary

| ID | Sev | Boundary | Claim attacked | Status |
|----|-----|----------|----------------|--------|
| RT3-01 | **High** | B4 host↔mgmt | "hostd authenticates by lease epoch" | CONFIRMED (executed) |
| RT3-02 | **High** | B4 | ready/node-cert bound to the lease holder | CONFIRMED (executed) |
| RT3-03 | **High** | internet→audit | "audit is the forensic record"; "a refused flood can't grow the chain" | CONFIRMED (code + probe) |
| RT3-04 | Medium | B4 | pool nodes belong to the host that joined them | CONFIRMED (executed) |
| RT3-05 | Medium | B4 | host audit records are signed and scoped | CONFIRMED (executed) |
| RT3-06 | Medium | B1 public | "facet classes: a fetch() through the bypass is still refused by the platform" | CONFIRMED (hermetic workerd) |
| RT3-07 | Medium | B1 public | public env guard: "no spawning" | PLAUSIBLE (code-traced) |
| RT3-08 | Medium | B1/B3 | "request.cf caller comes only from WhoIs"; `actingFor` is platform-set | CONFIRMED (hermetic workerd + code) |
| RT3-09 | Medium | internet | per-IP limits bound a single attacker | PLAUSIBLE |
| RT3-10 | Medium | B2 tenant↔tenant | public name ownership | PLAUSIBLE |
| RT3-11 | Medium | guest→vmhelper | the root helper validates its inputs | CONFIRMED arithmetic; crash PLAUSIBLE (Linux) |
| RT3-12 | Medium | B4 | deployment status is reported by its own host | CONFIRMED (executed) |
| RT3-13 | Low/Med | internet, B1 | `tw audit`/`tw ls` render untrusted strings safely | CONFIRMED (probe + code) |
| RT3-14 | Low | B4 | hosts see only their tenants | CONFIRMED |
| RT3-15 | Low | B4 | `tw host add` binds the record to the joining node | CONFIRMED (executed) |
| RT3-16 | Low | vmhelper | only a pool VM can become a snapshot base | CONFIRMED (code) |
| RT3-17 | Low | cron | "no overlap", 15-min limit | CONFIRMED (code) |
| RT3-18 | Low | internet | identity-header strip list matches the doc | CONFIRMED (probe) |
| RT3-19 | Low | operator | admit cache survives kill+purge | PLAUSIBLE |
| RT3-20 | Low | supply chain | releases are authenticated | CONFIRMED |
| RT3-21 | Low | policy | RT2-L1 fixed | still open |
| RT3-22 | Info | — | pool nodes "serve nothing"; warm-key TTL; expose-off RST; template defaults | noted |

**Framing.** The three Highs share one root: **a registered host is
trusted for every tenant on the mgmt, not just for the leases it holds.**
On a single-host `tw up` deployment that host *is* the operator and
RT3-01/02/04/05/12/14 are moot. They matter the moment a second host is
added (`tw host add`), because the host is the component most likely to
be compromised: it runs tenant code, and on macOS or Linux without
Landlock the Node tier is cooperative only (RT-08). THREAT-MODEL §2 B4
says "hostd authenticates by lease epoch"; the code does that for
credentials, kills and volume keys but not for object creation, ready, or
audit ingest.

## RT3-01 (High) — Any registered host can create or spawn objects in any tenant's workload

`POST /v1/objects` is role `host` (`api/openapi.yaml`, `x-tw-role`) and
`mgmt/v1.go:844` passes the body to `mgmt/service.go:1130 CreateObject`,
which resolves the deployment from the body's `tenant/workload/namespace`
and never compares the lease holder or `dep.HostID` with the calling
host. `ActingFor`, `Caps`, `TraceID`, `Parent` and `poolObjectID` all
come from the body.

**Executed** (throwaway test, two `tag:tw-host` principals through
`Server.TailnetHandler`, SQLite store, fake Tailscale API): host h2, with
workload `w` pinned to h1, `POST /v1/objects {tenant, w, A, "victim",
actingFor: "ceo@example.com"}` → **202**, a mint queued on tenant B's
tailnet credential; a spawn under it → **202**. Credentials stay fenced
(`Credential` → `checkFence` → `409 lease_lost`), so h2 cannot *join* the
node, but it burns tenant B's `maxObjects` quota and registration
throttle, mints one single-use auth key per call, inflates the warm-key
pool demand (`mgmt/warmkeys.go:131`), and writes a forged `actingFor` /
`origin: public` into mgmt's committed `spawn` audit record.

**Fix:** in `CreateObject`, `Ready` and `UpdateObject`, when
`p.Kind == "host"` require that `LeaseName(dep.TenantID, dep.Workload)`
is held by `p.HostID` (or `dep.HostID == p.HostID`); refuse body
`actingFor`/`traceID` from any other host.

## RT3-02 (High) — `ready` without `epoch` is unfenced: any host can mark any object ready, choose its node IDs, obtain a mgmt-signed node certificate for them, and destroy the real credential

`mgmt/service.go:1866` `if r.Epoch > 0 { fence = … }`; otherwise no
fence. `mgmt/v1.go:1018` only checks `status == "ready"`; the spec marks
`epoch` optional (`api/openapi.yaml`, `ObjectPatch`). The handler then
deletes custody (`:1902`) and signs the node cert (`:1917`). The real
hostd always sends `Epoch` (`hostd/objects.go:399`,
`hostd/poolnodes.go:210`), so the no-epoch path exists only for attackers.

**Executed:** h2 `PATCH /v1/objects/<victim> {"status":"ready",
"stableNodeID":"nEVIL","magicDNS":"evil.t.ts.net"}` → **200** with
`cert`; `GET /v1/certificates/nEVIL?tenant=…` → **200**, a mgmt-signed
certificate binding tenant B's object tags, `lim` and lineage to the
attacker's node ID. The legitimate h1 then gets `409 already_joined`. The
cert cannot widen caps past the receiver's WhoIs CapMap (RT-01
intersection holds), but it falsifies identity and lineage and is a
one-call DoS per object.

**Fix:** make `epoch` required for `status: ready` (spec + handler), and
require the fence holder to be `p.HostID`.

## RT3-03 (High) — An anonymous public flood (or hostile tenant code) silently breaks a host's audit chain, permanently

Two independent reviewers reached the same sink from different sources.

- **Sink.** `hostd/audit.go:52-55`: the pending queue is capped at
  10 000 by dropping the *oldest already-hashed* records, while `a.prev`
  keeps advancing. The next submitted record's `prevHash` points at a
  dropped record; mgmt stores it as submitted and `VerifyChain`
  (`mgmt/audit.go:125`) reports `BrokenAt`. From then on `tw audit`
  exits `audit_unverified` for that host (`cmd/tw/ops.go:636`). Dropped
  records include `deny`, `egress` and `principal` — exactly the evidence
  the chain exists to keep. Flush happens only after a *successful*
  heartbeat (`hostd/host.go:383-404`), so any mgmt unreachability widens
  the window.
- **Source A, internet.** Every forwarded public request *and every
  `404 not_public` / `400 bad_request`* is one `public_request` record
  (`netd/public_serve.go:598-623`); only 429/413/431 are rolled up.
  Probe: 20 GETs to `/nope` → 20 records. At the default 50 rps a ~200 s
  mgmt outage, or a few public workloads at the 1000 rps ceiling, fills
  the queue. `docs/public-ingress.md` ("a refused flood can't grow the
  chain") is true only for limit refusals.
- **Source B, tenant code.** `ctx.tailnet.audit` accepts 64 notes per
  call with no per-tenant rate limit (`hostd/http.go:160-161`); ~3 300
  × 4 KiB records/s of unbounded mgmt storage.

**Fix:** never drop host-source records (separate queue, or drop
`tenant_note`/`public_request` first and emit one in-chain
`audit_dropped{n}` record); roll `not_public`/`bad_request` into the
per-minute summary; token-bucket tenant notes; backpressure instead of
truncation. Add a `VerifyChain` test that a dropped record is detected
*and attributed* rather than reported as corruption.

## RT3-04 (Medium) — Pool-node theft across hosts

`mgmt/service.go:1349 usablePool` checks tenant/workload/class/tags, never
the caller; `TransferPool` runs inside `CreateObject` (`:1302`).
**Executed:** h1 pre-joins `_pool/A/p1` and reports ready; h2
`POST /v1/objects {…, "poolObjectID": "<p1>"}` → **202 status=assigned
stableNodeID=nPOOL**. The node's state lives on h1's disk, so the object
is stranded and h1's pool is drained one call at a time. Same fix as
RT3-01 (bind pool transfer to the lease holder, and to the host that
reported the pool node ready).

## RT3-05 (Medium) — Audit ingest: `hostSig` is optional, and a host may name any tenant's object and any `actingFor`

`mgmt/service.go:2612-2616`: signatures are verified only `if
host.AuditPubKey != ""`; a host registers (and re-registers) its own
key (`:481`). Ingest attributes the record to the tenant found by the
body's `objectID` (`:2595-2598`) with no lease check. **Executed:** h2,
registered without a pubkey, submitted an unsigned `allow` record for
tenant B's object with `actingFor=ceo@example.com` → **201, chain ok**;
it appears in `GET /v1/audit?tenant=B&human=ceo@…` as `source: host,
chain: host:h2`. Chain provenance is intact (RT-02 scope was tenant
code; this is the host tier). **Fix:** refuse `AppendAudit` from hosts
without an `AuditPubKey`; verify every `hostSig` at ingest; refuse or
flag records whose `objectID` is not on a lease the host holds.

## RT3-06 (Medium) — The facet "defense in depth" against public creates is keyed on tenant-controlled `request.cf`

`docs/public-ingress.md` says that for facet classes "a fetch() through
the bypass is still refused by the platform". It is not. The Host DO
consults hostd with `public: true` only when `isPublic(req)` reads
`req.cf.tailscale.requester` (`hostd/gen/js/platform.js:251-259`); a
cf-less call takes the normal create path (`__facet(!who)` → `__ensure()`
→ hostd `ensure` with `wake: true`, `platform.js:194-202, 276`,
`hostd/http.go:238-260`), and every RPC property on the Host Proxy goes to
`__facet(true)` with no public check at all (`platform.js:174-183`).
**Executed hermetically** (stock workerd 1.20261108.1 in `/tmp`):
`stub.fetch("http://d/")` delivers `cf: null`, and `import { env } from
"cloudflare:workers"` works at the compat date. Scenario: Funnel request
→ tenant Worker that reaches raw bindings (the `mcp` template itself
caches `ENV = env`, `templates/mcp/src/index.ts:20`) →
`env.Room.getByName(n).anyRpc()` → one object and one tailnet node per
name, bypassing `admit`, `maxObjects` and the 20/min lane. **Fix:** carry
the requester kind on the platform's own ingress path (`cf.tw`, which
only hostd sets) rather than on the forwarded request's `cf`, and make
`ensure` refuse creation unless the platform entry path attests
non-public.

## RT3-07 (Medium) — `env.__TW` reaches tenant code unguarded, so the public env guard is cosmetic for deliberate code

`publicEnv` copies `{...env}` including the `__TW` service binding
(`js/packages/tailnet/src/runtime.ts:322-330`; `wrapHandler` /
`wrapPlainClass` pass raw `env`, `:211, 395`), while `wrapFacetClass`
deletes it (`:180-181`). `env.__TW.call("spawn" | "ensure" | "ready",
{class, name})` reaches hostd as the workload (`hostd/http.go:298-345`),
and `ready` creates objects of any node class, facets included. From a
public request this mints nodes without any guard. PLAUSIBLE (traced,
not run). **Fix:** strip `__TW` from every tenant-visible env; have
hostd refuse create-class ops when the request chain began public (ties
to RT3-06); restrict `ready` to plain classes.

## RT3-08 (Medium) — `request.cf` is forgeable in-process; tenant code sets `actingFor` on objects and children

Two findings with one root. (a) `env.D.getByName("x").fetch(new
Request(url, { cf: forged }))` and RPC `echo(new Request(url, {cf}))`
deliver the forged blob verbatim (**executed hermetically**). On tailworks,
`getCaller()` in a sibling facet or plain DO returns the forged identity
and `caps()` (`js/packages/tailnet/src/caller.ts:54-82`, no provenance
check beyond `nodeId` presence), so `authorizeTool` passes;
`principalOf(req)` in the Host DO (`platform.js:44-50, 261-273`) sends
`actingFor: "ceo@victim"` to hostd, which writes a **source: host**
`principal` record and sets `o.ActingFor` (`hostd/http.go:273-281`).
(b) `ctx.tailnet.spawn(ns, {principal})` is passed through
(`hostd/gen/js/tw-rt.js:131`) and `hostd/http.go:316-320` uses it as the
child's `actingFor` when set, falling back to the parent's only when
empty; `ensure` takes `actingFor` from args the same way
(`http.go:243-258`). STATUS.md:554/612 claims `actingFor` "comes from the
platform". Netd's stripping is intact, so the scope is one workload's
trust domain (same class as RT-04), but THREAT-MODEL §4(2)/§5 state the
opposite and the forged principal lands in host-signed audit. Not an
authorization input today (OBO unimplemented). **Fix:** seal the netd
caller blob (HMAC under the instance MAC key, verified by the runtime
before `getCaller` honours it); never derive `actingFor` from a forwarded
request's `cf`; in `spawn`/`ensure` accept `principal` only if equal to
the parent's.

## RT3-09 (Medium) — `maxConns` is per node with no per-IP cap

`netd/public_serve.go:238-249` counts every admitted connection against
`MaxConns` (default 256) and RSTs the rest; keep-alive connections live
up to `IdleTimeout: 60s` (`:296`) and `ipRps` gates only *requests*
(`:575-586`). 256 idle TLS sockets from one address close the workload's
public ingress for everyone. Upgrade requests on an `upgrade` route have
no request timeout (`:659`), so WebSocket slots are held indefinitely.
PLAUSIBLE (not run at scale). **Fix:** per-IP connection cap, shorter
idle timeout on the public listener, bounded upgraded-connection lifetime.

## RT3-10 (Medium) — Cross-tenant public-name squatting and interception on a shared tailnet

The public node's hostname is the bare workload name for every tenant
(`mgmt/service.go:1234-1237`); only `tw-mgmt`/`tw-host*` are reserved
(`mgmt/public.go:45-47`), and mgmt never checks that another tenant
already publishes the name. The loser joins as `<name>-1` and is refused
(`hostd/objects.go:375-392`, D6). So tenant B deploying public `foo`
first owns `https://foo.<tailnet>.ts.net`; if A had it and loses it
across a kill/redeploy (`purgeDeadPublicNode`), B now receives A's
webhooks with a valid certificate. PLAUSIBLE (needs two tenants and a
real tailnet). **Fix:** mgmt-level uniqueness of `(tailnet, public
name)` with first-owner persistence; refuse a deploy whose name another
tenant owns.

## RT3-11 (Medium) — The root vmhelper trusts the VMM's uffd region table

`hostd/vm/helper/uffd_linux.go:149` checks only that `PageSize` is a
power of two and `r.Offset+r.Size > len(u.mem)`; the sum can wrap, and
`Base` alignment, `Size > 0`, `Size % PageSize` and overlap are
unchecked. `copyPage` (`:212-215`) and `prefetch` (`:247`) compute
`src := r.Offset + (page - r.Base)` and index `u.mem[src]`; with a
wrapped `Offset` or an unaligned `Base` the index is ~2^64 and the
goroutine panics (**executed**: `index out of range
[18446744073708503040]` in a throwaway test of the arithmetic). The
handshake comes from Firecracker (the jail uid), i.e. from a tenant that
has escaped its guest into the VMM. A panic kills the helper process,
which is root and shared by every VM on the host: every tenant's helper
connection drops, and the base-snapshot and volume busy-map state with
it. Not discussed in ADR-030. **Fix:** validate `Base % PageSize == 0`,
`Size > 0 && Size % PageSize == 0`, `Offset <= len && Size <= len -
Offset`, non-overlap; `recover()` in `serve`/`prefetch` and fail the one
session, never the process.

## RT3-12 (Medium) — Heartbeat lets a host set any deployment's host-reported status

`mgmt/service.go:535-539` applies every `deployments[]` entry by ID
(`sqlstore.go:154 SetDeploymentStatus`). **Executed:** h2 `PUT
/v1/hosts/h2/status {deployments: [{deploymentID: <h1's>, status:
"failed", error: "pwned"}]}` → admin `GET /v1/deployments/{id}` shows it.
Display and CLI-wait only, not placement. **Fix:** `… WHERE id=? AND
host_id=?`.

## RT3-13 (Low/Medium) — Terminal injection into `tw audit` and `tw ls`

`publicspec.CanonicalPath` returns the decoded path and refuses only
`%2F`/`%5C`/dots/`//` (`internal/publicspec/publicspec.go:261-284`);
control characters pass. **Probe:**
`/webhooks/%1b%5b31mFAKE%0a2026-01-01%20kill%20by%20admin%20forged` →
200, recorded path contains ESC and LF. `cmd/tw/ops.go:409` prints
`d["path"]`/`d["method"]` with `%v`; `tenantNoteText` prints note
`detail` raw (`:452-468`); `describe` prints `d["name"]` raw for
`create`/`public_object_created` (`:376, 428`); `tw ls` prints `o.Name`
raw (`:30-38, 221`); names are bounded to 256 bytes only
(`mgmt/service.go:1136`). An unauthenticated internet client can forge
visually convincing audit rows or recolour/clear the operator's
terminal. **Fix:** `strconv.Quote` or strip C0/DEL/ESC in every CLI
renderer; refuse control bytes in `CanonicalPath` and in object names at
`ensure`/`admit`.

## RT3-14 (Low) — Any registered host enumerates every tenant and object

`GET /v1/tenants[/{id}]`, `GET /v1/objects[/{id}]`,
`GET /v1/objects/{id}/certificate`, `GET /v1/certificates/{id}` are
`admin|host` with no tenant scoping. Exposed: `apiKeyFile` /
`oauthClientFile` paths, OAuth/WIF client IDs, OBO public key,
mint-queue depth, every object's hostname/MagicDNS/tags/caps/cert/
lineage. No secrets. **Fix:** scope host reads to tenants whose leases
the host holds.

## RT3-15 (Low) — A pending `tw host add` record is claimable by any unregistered `tag:tw-host` node

`mgmt/service.go:463` refuses only when `old.NodeID != "" && != nodeID`;
pending rows have `NodeID == ""`. **Executed:** admin `POST /v1/hosts
{hostID: "h9"}`, then a stranger node `PUT /v1/hosts/h9` → **200**,
inheriting h9's pool/tier intent. **Fix:** bind the record to the join
key's node.

## RT3-16 (Low) — vmhelper's "only a pool VM can become a base" guard is weaker than stated

`hostd/vm/helper/session_linux.go:579-581` refuses only `!sess.slot2 ||
sess.vol != nil`; `slot2` stays true after a claim and `vol` is nil for
any workload without a DO volume, so a loaded tenant VM (bundle + MAC key
in RAM) is accepted as a base by the helper. hostd-side barriers
(`fsscan` first, only on a fresh `base` boot,
`hostd/vm/snapshot_linux.go:139-152`) are the real guard; hostd is
trusted (B4), so defence in depth only. **Fix:** a `Base: true` start
flag the helper remembers; refuse `attach` on such sessions and
`snapshot-base` on any other.

## RT3-17 (Low) — Cron "no overlap" and the 15-min limit are enforced at hostd's request layer only

`hostd/cron.go:150-155` clears `cronRunning[expr]` when `invokeCron`
returns, and `:213-217` returns on the context deadline without aborting
the platform-side `scheduled()` (`platform.js:94-105` awaits
`env.TENANT.scheduled` with no cancellation). The next minute starts a
second run while the first is still inside workerd. Lease fail-closed
and the fresh-heartbeat gate hold (`:61-69, 76-78`). Documented only as
"best effort". **Fix:** abort or restart the instance on timeout, or keep
the running mark until the platform answers.

## RT3-18 (Low) — The strip list is narrower than the doc

Doc: `Tailscale-*` is stripped. Code (`netd/ingress.go:125-138`): four
`Tailscale-User-*` names plus `Tailscale-App-Capabilities`. **Probe:**
`Tailscale-Ingress-Src`, `Tailscale-Node-Caps`, `X_TW_CF` and `Via`
reach the upstream. No consumer exists today and the `requester`/`caller`
blob is built from scratch after stripping, so no identity impact.
**Fix:** strip by prefix `tailscale-`, `x_tw`, `tw_`.

## RT3-19 (Low) — Admit cache survives kill + purge

`admitted` is a per-isolate `Map`, never invalidated
(`hostd/gen/js/tw-rt.js:263-280`); `killLocal` deletes `byKey`
(`hostd/objects.go:504`) and `forget()` only frees pubCreates for
`ActingFor == "public"` objects (`:525`). For a plain-DO class, after a
kill and a mgmt purge, a cached name goes straight to `real.fetch` → DO
constructor → unflagged `ensure` → new node. Bounded by names that
previously existed; facets are protected by `__ensurePublic`. **Fix:**
evict the admit cache on `admin kill`, or TTL it.

## RT3-20 (Low) — Releases are integrity-checked, not authenticated

`install.sh` verifies the tarball's own `SHA256SUMS`
(`scripts/install.sh:90-95`); darwin binaries are ad-hoc signed
(`release.sh:121-128`). No minisign/cosign signature over
`dist/SHA256SUMS`, no CI, builds come from a developer Mac. **Fix:** sign
`SHA256SUMS` and verify in `install.sh`.

## RT3-21 (Low) — RT2-L1 is still open

`internal/policy/policy.go:171` `okTag` is still a bare
`HasPrefix(t, "tag:tw")`; `security/attacks/rt06_policy_json_tricks_test.go:51`
still `t.Skip`s.

## RT3-22 (Info)

- Unassigned pool nodes emit disco pings as path-measurement vantage
  points (`hostd/paths.go:169-173` does not skip `isPool()`); STATUS says
  a pool node "serves nothing". Benign. Fix: skip pool nodes.
- Pool-node count is bounded only by tenant `maxObjects`; retirement is
  host-side policy (`hostd/poolnodes.go:117-140`). Documented in ADR-031.
- Warm keys are preauthorized, single-use, 30-minute keys
  (`mgmt/warmkeys.go:38`), up to 5 per hot class, stored only in custody.
  A custody compromise now yields usable join keys for up to 30 min
  rather than 5; `tw doctor` could surface the count.
- `tw expose off` sends FIN, not RST, on public flows (`netd/kill.go:259-270`
  type-switches on `*gonet.TCPConn`/`*net.TCPConn`; public flows are
  `*limitedConn{*tls.Conn{FunnelConn}}`). Still sub-2 ms; an already
  dispatched request finishes in workerd and its response is dropped.
- Templates: `ai-agent` lets any tailnet caller address any agent by name
  (`templates/ai-agent/src/index.ts:74-76`), one node per name; `mcp`
  caches `env` in a module global (`templates/mcp/src/index.ts:20`), the
  pattern the public-guard doc warns about. No hardcoded keys. Fix: key
  agents by `principalKey(who)`; don't cache `env`.
- On macOS the Seatbelt profile allows unix-socket connects within the
  instance dir (`hostd/seatbelt.go:140-141`), which holds `ingress.sock`;
  a post-V8-escape process could POST `/_tw/scheduled|admin` with a
  forged `x-tw-cf`. Intra-workload only (RT-04 class); Landlock closes
  it (`seatbelt.go:253-258`).
- Double-decoding is on the app's side only: `/webhooks/a%252Fb` reaches
  the Worker as `a%252Fb` (probe); an app that decodes twice could see
  `/`. netd's gate is on the once-decoded path.

## Controls verified to HOLD in Pass 3

- **RT-01/02/03/05/06 fixes still hold on master.** Socket token
  required even for healthz (`mgmt/server.go:311-328`); tsnet healthz
  without WhoIs → 401 (`:393`); policy additive-only (`go test
  ./internal/policy` green); node cert signed at ready
  (`mgmt/service.go:1917`), cleared on remint (`:1969`); tenant audit
  records forced to `tenant_note`/`source: tenant` (`hostd/http.go:171-194`),
  mgmt refuses mismatches (`mgmt/service.go:2548-2563`); per-object MAC
  keys + nonce cache (`hostd/gen/mac.go:37-41, 83-122`), `MAC_KEY`
  platform-only (`gen.go:313`), regenerated per instance generation so a
  restart invalidates old MACs.
- **mgmt.** Admin requires `github.com/rosskukulinski/tailworks/cap/admin` or `-admins`
  (`server.go:185-198`); hosts bound to their registered NodeID
  (`:407-414`) and to `/v1/hosts/{id}` (`v1.go:1316`); credentials,
  remint, kill and volume keys fenced on holder+epoch
  (`sqlstore.go:1382-1397, 1481`; `v1.go:1085`; `service.go:2804`); a
  hostile heartbeat claiming another's lease gets `revoke`; stateful
  takeover guard fails closed without a bundle (`service.go:909`);
  placement reports are host-bound and limited to the host's tenants
  (`placement_service.go:71`); public gate defaults to deny
  (`public.go:67, 209`); Service takeover refused on exact comment match,
  validated names, case-insensitive MagicDNS collision check
  (`services.go:113, 133`); tailworks never writes `autoApprovers.services`;
  `funnel` nodeAttr refused for anything but `tag:tw-public`/
  `tag:tw-issuer` (`internal/policy/policy.go:219-249`); warm/host keys
  single-use, preauthorized, bounded expiry, secrets only in custody
  (`warmkeys.go:222-228`); no key material in Postgres; custody/issuer
  files 0600 mode-checked (`custody.go:65,79`, `issuer.go:59,70`).
- **Snapshot restore (ADR-030).** The base contains kernel + guestd +
  an exited warm-up workerd with AppDir/tmp/shm emptied
  (`guest/tw-guestd/main_linux.go:197-205`); hostd refuses a base unless
  `fsscan` finds no state (`snapshot_linux.go:146-152`); no bundle, MAC
  key, node key, token, auth key or env var is in it (all arrive in
  `config.capnp` at `load`, `vm_linux.go:573-596`). On `load` the guest
  does `RNDADDENTROPY` + `RNDRESEEDCRNG` with 64 host bytes before
  anything else (`main_linux.go:320-324, 359-381`), bind-mounts a fresh
  `boot_id` (`:384-396`), `settimeofday` from a stamp taken just before
  send (`vm_linux.go:596`); the tenant's workerd is a new process after
  the reseed, so V8/crypto/TLS randomness is fresh; the LUKS key never
  enters the guest (`vm_linux.go:552-560`). Base files are root 0600 in
  0700 dirs, a new dir per rotation, removed on holder-conn close
  (`helper_linux.go:199, 364-372, 132-148`; `session_linux.go:590, 608-610`);
  memory is served by `UFFDIO_COPY` from a `PROT_READ|MAP_PRIVATE` map
  into the VM's own hugetlb pages (`uffd_linux.go:89, 215-216`), no
  cross-VM page sharing; the base VM is killed after the snapshot.
  ADR-030's residual (cloned KASLR/net_secret/siphash across tenants for
  ≤1 h) matches the code.
- **Warm VM pool.** Netns has no host address/route/listener
  (`helper/netns_linux.go:20-27`); gateway RSTs every SYN until claim
  (`vnet_linux.go:104-106`); claim injects volume (epoch-fenced),
  handlers, bundle, load (`vm_linux.go:544-600`); single use enforced on
  both sides (`pool_linux.go:243-258`, `main_linux.go:282, 306-312`).
- **Pool nodes.** Created with exactly the class's tags, never
  ephemeral/spawn (`mgmt/service.go:1152-1158, 1239`); transfer re-checks
  tenant/workload/namespace/class/ready/tags (`:1357-1372`) in one
  conditional update (`sqlstore.go:1221-1232`); Ready must name the
  assigned stable node (`:1888-1897`); cert resolves by stable node ID to
  the new object and is signed at once (`mgmt/obo.go:140-171`); receivers
  fail closed (`netd/ingress.go:64-83`); pool nodes serve nothing
  (`hostd/http.go:59`), tenant code cannot act as `_pool`
  (`objects.go:154-158`, `service.go:1351`).
- **Public ingress.** Non-Funnel connections closed, `Src` from the
  transport only (`public_serve.go:224-237, 550-557`); order is limits →
  route → strip → blob; forged `x-tw-cf`/`x-tw-object`/`Forwarded`/
  `X-Forwarded-*`/`CF-*`/`Proxy-Authorization` all stripped (probe);
  no `caller` key in a public blob; `getCaller` throws `public_request`
  (`caller.ts:55-60`), `principalKey` refuses public; public requests
  reach only `kind: "workload"`, `/_tw` refused twice
  (`publicspec.go:280-282`, `platform.js:128-135`); path tricks (`..`,
  `%2e%2e`, `//`, `%2F`, `%5C`, `\`, `..;`, `?#`, U+2024, trailing dot)
  refused or inert; HTTP/1.1 only on Funnel (no `NextProtos`); streamed
  body cap via `MaxBytesReader` (`:651-691`); upgrades only on `upgrade`
  routes; the create guard admits by name only, refuses
  `newUniqueId`/`get(id)`, persists `admit` atomically
  (`tw-rt.js:281-316`, `hostd/public.go:274-303`); `maxObjects` + the
  20/min lane are atomic under `pc.mu` (`hostd/public.go:98-127`);
  deploy refused under `deny`, host can't mint a public node under
  `deny`, assignment drops `Public` → listener closed
  (`mgmt/public.go:60-71, 200-219, 94-96`; `hostd/public.go:177-186`);
  `public_rejected` rollup 1/min/node with a 10 000-IP cap; cert
  prefetch bounded and not attacker-triggerable.
- **Platform wrapper.** No egress side channel: `connect()`/`node:net`
  become CONNECT to the globalOutbound service and are refused (hermetic;
  netd also refuses CONNECT, `egress.go:66-68`); Cache API unconfigured;
  redirect-follow and WebSocket upgrades re-enter globalOutbound;
  platform worker `globalOutbound = "deny"` (`gen.go:317`); no
  `cacheApiOutbound`/`unsafe` modules; tenant bindings are DO namespaces,
  `__TW` and vars only (`gen.go:349-367`). `scheduled` kind set only by
  hostd (`hostd/cron.go:210`), declared crons only (`platform.js:94-99`);
  cron runs count as in-flight and go through the same spawn budget.
- **Bundles and config.** `Unpack` rejects non-regular entries, `..`,
  absolute paths, backslash, quote/CR/LF/NUL, >64 MiB
  (`hostd/bundle/bundle.go:418-431`); guest `extractTar` skips `..`
  (`main_linux.go:507-515`); every tenant string reaches capnp via `cq`
  and JS via `jsq` (`gen.go:398-427`); names regex-validated
  (`bundle.go:160-161, 262-267`; `gen.go:103-113, 363-366`); no capnp
  injection found.
- **CLI and supply chain.** `tw dev` binds 127.0.0.1 only
  (`cmd/tw/dev.go:169`); `tw link` is client-only, 0600 state,
  WhoIs-authenticated; no sudo in `tw up --yes`/`doctor`; policy written
  only with `--policy-apply` through the RT-06 checks; OAuth file
  0600-checked (`up.go:233-243`); lockfile present, workerd pinned and
  integrity-checked at release (`release.sh:29, 69-80`); `@tailworks/*` have no
  third-party deps; no postinstall, no `curl | sh`, no submodules; bundle
  upload is `dist/index.js` + `tailworks.jsonc` + `wrangler.jsonc` only, never
  `.env` (`cmd/tw/app.go:212-226`).

## Fix status (same day, 2026-11-14)

| ID | Status | Where |
|----|--------|-------|
| RT3-01 | **Fixed** (F1): host creates are fenced on the workload lease (holder always, epoch when sent; hostd sends it) in the insert transaction | `mgmt/service.go` `CreateObject`, `mgmt/sqlstore.go` `createObjectTx`; `mgmt.TestRT3_HostWritesAreLeaseBound` (fails on `4f23a96`) |
| RT3-02 | **Fixed** (F1): `ready` is fenced on the holder for every host caller | `mgmt/service.go` `Ready` |
| RT3-03 | open (F2 queued) | — |
| RT3-04 | **Fixed** (F1): pool transfer fenced; the pool node must have been joined by the caller | `mgmt/service.go` `usablePool`, `poolJoinedBy` |
| RT3-05 | **Fixed** (F1/F3): audit ingest needs the host's registered key, verifies every `hostSig`, refuses objects of workloads the host never served | `mgmt/service.go` `IngestAudit` |
| RT3-06 | **Fixed** (F4): the Host DO asks hostd before any tenant code runs, for fetch and RPC; hostd creates only admitted names | `hostd/gen/js/platform.js`, `hostd/objects.go` `ensure`; `hostd.TestPublicIngress` §3b, `gen.TestGeneratedWrapperPublic` |
| RT3-07 | **Fixed** (F4): `__TW` stripped from every tenant env; Worker-level spawn needs the ticket; `ready` never creates | `js/packages/tailnet/src/runtime.ts`, `hostd/http.go` |
| RT3-08 | **Partial** (F4): `principal` on spawn is `inherit`/`none` only; `actingFor`/`public` stripped from tenant op args. In-process `request.cf` forgery toward siblings (`getCaller`, Host DO `principalOf`) remains: F5 | `hostd/http.go` spawn, `platform.js` `TailnetApi` |
| RT3-12 | **Fixed** (F1): deployment status updates are scoped to the deployment's host | `mgmt/sqlstore.go` `SetDeploymentStatus` |
| others | open; see `security/LEDGER.md` | — |

Design note for F4: `docs/create-admission.md`.

## Regression tests

`mgmt/rt3_hostfence_test.go` (F1), `hostd/gen/ticket_test.go` and
`hostd/public_test.go` §3b (F4), `js/packages/tailnet/test/public.test.js`.
The remaining items are turned into tests by the loop in
`security/LOOP.md`.

---

# Note 2026-11-17: macOS Seatbelt profile tightened (#121, PR #171)

This note updates the macOS parts of RT-08 (Pass 2 verdict "cooperative
elsewhere", and "Residual risk by tier: macOS process") and the Pass 1 fix
status row. The earlier passes are left as written.

The default macOS process-tier profile is now `seatbeltProfile` in
`hostd/seatbelt_strict.go`. It keeps `(allow default)` for system services
(Mach, sysctls), then:

- `(deny file-write*)`; writes allowed back only in the workload's own
  dirs and `/dev/null`, `/dev/zero`, `/dev/dtracehelper`;
- `(deny network*)`; allowed back: unix sockets in its own dirs, TCP
  connect to `localhost:<port>` for the ports hostd passes (a Node app:
  its egress proxy and its own port; workerd: none), TCP bind and accept
  on its own port. No UDP, so no DNS of its own;
- `(deny signal)` with `(allow signal (target same-sandbox))`: itself and
  its children, not tw-hostd, tw-mgmt or another workload;
- `(deny process-exec*)`; exec allowed of the runtime, the Node prefix, its
  own dirs and `/System`, `/bin`, `/opt`, `/sbin`, `/usr`;
- reads: the deny list as before (home dir, tailworks' state and run dirs,
  hostd's data and run dirs, tenants' credential files).

| ID | Verdict on macOS (default `-sandbox seatbelt`) | Evidence |
|----|------|------|
| RT-08 | **HOLDS on macOS**, with two residual gaps (H26, [#175](https://github.com/rosskukulinski/tailworks/issues/175)) | `hostd.TestSeatbeltStrictProbes` runs each probe unsandboxed first (it must succeed), then under the profile with `sandbox-exec`: TCP connect to a loopback port it wasn't given, TCP to `1.1.1.1:443`, TCP bind of another port, `kill -0` of another process and of the test process (hostd), a write outside its dirs and to `/tmp`, a read of a denied dir, exec of a script outside the allowed dirs and a unix connect outside its dirs all fail; its given TCP port, its own port bind, signalling itself and a child, its own dir, `/dev/null`, its own unix socket, `workerd --version` and node listening and connecting to the "proxy" work. `hostd.TestSeatbeltStrictProfileShape` pins rule order (Seatbelt applies the last match) and that no other TCP allow appears. `hostd.TestMultiTenant` repeats loopback TCP, `kill -0` hostd and a `/tmp` write under tenant A's live workerd profile. `TestNodeTier/Sandbox` (`make test-node`, not in CI) probes from inside the sandboxed Node app: internet TCP, host loopback, hostd's push socket, a signal to hostd and a `/tmp` write are `EPERM`, and status reports `Egress: enforced`. `hostd.TestEgressEnforcementByABI` pins `seatbelt` → `enforced`, `seatbelt-legacy` → `unenforced`. |

Still open on macOS (H26, #175):

- **Reads are a deny list.** Outside the denied dirs a workload reads what
  the user can (`/etc`, `/tmp`, …). Landlock allows only OS dirs, the
  runtime and its own dirs.
- **TCP is matched as `localhost:<port>`**, not one address: a workload
  reaches its allowed port numbers on any loopback address.

`tw-hostd -sandbox seatbelt-legacy` keeps the previous files-only profile
for one release; under it the Pass 2 macOS verdict (TCP anywhere, writes to
`/tmp`, signals to any same-user process, unix sockets outside the deny
list including the system tailscaled socket, egress `unenforced`) applies
again.
