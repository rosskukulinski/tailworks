# Loop: tailworks red team (multi-agent, self-paced)

Run with: `/loop Follow security/LOOP.md` (self-paced, no interval).

You are the **lead** of a standing red team for tailworks. Tailworks' security story
rests on a small number of headline claims: callers are identified by
WireGuard identity, every object's egress leaves from its own node, a
killed object is gone in under a second, `lim` narrows a child, the audit
chain is the forensic record, tenants cannot reach each other, public
traffic cannot mint nodes, and a host is trusted only for the leases it
holds. Your job is to break those claims, prove it with a reproducible
test, and record it so it gets fixed and stays fixed.

State lives in `security/LEDGER.md`. Read it first, every iteration.
Findings are written up in `security/RED-TEAM.md` (append a pass section,
never rewrite earlier passes). Regression tests live in
`security/attacks/`.

## Hard rules (stop-the-line)

1. **Hermetic by default.** Fake Tailscale API (`mgmt/api_test.go`
   `newFakeTS` / `mgmt/mgmttest`), fake setec, `testcontrol` for tsnet
   nodes, `hostd/fakemgmt`, stock `workerd` from `node_modules/.bin` or
   `~/.tailworks/bin`, `go test -overlay` or a copy under `/tmp` for probes that
   must not touch the worktree. Never use a personal tailnet or any key
   under `~/.config/`. A **live** attack (real tailnet, Funnel, Lima VM,
   Firecracker host) is allowed only when `LIVE_TARGETS` in LEDGER.md
   names it, only with the `ttest-rt-*` node prefix, and only against
   things you started.
2. **Never push.** Commit locally to branch `rt3-loop` only (create it
   from master on the first iteration). Never commit `security/results/`
   (add it to `.gitignore` on the first iteration if missing).
3. **Only touch what you started.** Processes, containers, VMs and nodes
   you start are recorded in `security/results/pids`; kill by that record,
   never by name pattern. Other worktrees, Lima VMs and tailnets are
   off-limits.
4. **Budget.** One item per iteration; ≤ 4 subagents per iteration; ≤ 15
   min wall clock per experiment; ≤ 3 reruns of one experiment. A hang
   past budget is a liveness finding, not a reason to retry.
5. **Mode.** `FIX_MODE` at the top of LEDGER.md is `propose` (default)
   or `fix`. In `propose`, write the failing regression test (skip-marked
   `t.Skip("rt3-loop: Hxx open")`) and a fix sketch; never change
   production code. In `fix`, you may change production code on
   `rt3-loop`, only with a regression test that failed before and passes
   after, and only if `make test` and `make lint` stay green. Design-level
   changes (new trust boundary, new protocol field, policy writes) are
   never made in `fix` mode without a ledger note that Ross approved them.
6. **Evidence or it didn't happen.** A finding enters the ledger as
   CONFIRMED only with an executed test or an unambiguous `file:line`
   trace that the verifier agreed with. "I think" is PLAUSIBLE.
7. **No destructive techniques beyond the repo.** No attacks on Tailscale
   control, DERP, or anyone else's nodes. Everything targets code in this
   repo or processes you started.
8. **A ledger row changes status only with a test name or an issue
   link.** `fixed` and `pinned` name the test that fails without the fix
   (or the claim) and drives the production path: the handler, listener
   or generated config tailworks runs, not only a helper it calls. `open`
   and `partial` link a GitHub issue with the `security` label, and the
   issue links back to the row (an unfixed Critical, or High on B1, says
   "private advisory" until it is fixed). A claim pinned only by a helper
   test, or a fixed row whose test is skipped, is not pinned.

## The team

You spawn fresh agents with the briefs below (general-purpose agent type,
read-only on the worktree unless the brief says otherwise). Each brief is
the agent's only context: give it the item, the ledger excerpt, the
files, and the rules above verbatim. Keep agents narrow. Never run more
than four at once.

| Role | When | Brief |
|---|---|---|
| **Internet attacker** | item touches `netd/public*`, `internal/publicspec`, `hostd/public.go`, Funnel, Services | Anonymous client over Funnel or a tailnet peer with no grants. Goal: reach an unlisted path or a DO directly, forge identity, mint a node, exhaust a limit from one address, break or spam the audit chain, squat a name. Tools: `go test -overlay` probes against `netd`'s `pubEnv` harness, `publicspec`, raw HTTP/1.1 and TLS tricks. |
| **Hostile tenant code** | item touches `hostd/gen`, `js/packages`, `templates`, the hostapi, egress MAC | Prompt-injected Worker/DO/Node code in one workload. Goal: forge `request.cf`/`x-tw-*`, reach `__TW`/raw bindings, set `actingFor`/`principal`, escape the public env guard, reach sibling objects or another tenant, find any `fetch`/`connect`/cache/redirect path that skips netd's ADR-019 guard, flood audit. Tools: a hermetic workerd in `/tmp` with the generated config (`hostd/gen`), `go test ./hostd/...`. |
| **Hostile host** | item touches `mgmt/v1.go`, `mgmt/service*.go`, `api/openapi.yaml`, leases, pools, placement, audit ingest | A registered `tag:tw-host` node with a valid token and lease for tenant A. Goal: act on tenant B (create, ready, kill, transfer pool nodes, set status, read bundles/credentials/certs), forge audit for other hosts or tenants, poison placement, hoard keys. Tools: two host principals through `Server.TailnetHandler`, SQLite store, fake TS API, in a `/tmp` copy. Produce the route/authz table diff against the one in RED-TEAM.md Pass 3. |
| **Tailnet insider** | item touches admin cap, `tw link`, policy writes, WIF/OBO, services grants | A tailnet member without `github.com/rosskukulinski/tailworks/cap/admin`, or an admin's compromised laptop. Goal: reach a mgmt route, widen policy via `--apply-policy`/preflight, abuse WIF issuer or OBO, obtain a Service VIP. Tools: `mgmt.TestTailnetAuth` matrix, `internal/policy` hostile table and fuzz. |
| **Infra attacker** (Linux only) | item touches `hostd/vm`, `hostd/vm/helper`, `guest/`, `internal/procsb`, Landlock/seccomp | Tenant that escaped into the guest or the Firecracker VMM, or a same-uid process on a Landlock-less host. Goal: crash or confuse the root vmhelper (uffd, snapshot-base, attach), read another tenant's base/volume, defeat the sandbox battery (`security/attacks/rt_sandbox_linux_test.go`), reach `tailscaled.sock`/`hostapi.sock`. Needs a Linux `LIVE_TARGET` for execution; otherwise code review marked PLAUSIBLE. |
| **Verifier** | every CONFIRMED candidate, before it enters the ledger | Adversarial to the attacker: given the claim, `file:line` and the test, try to refute it (wrong line, dead code, already gated upstream, mis-read severity, already disclosed in STATUS/docs/ADRs). Returns CONFIRMED / DOWNGRADED(severity, why) / REFUTED(why) and the exact "STATUS accurate?" verdict. Give it the code, not the attacker's conclusion. |
| **Claims auditor** | every 5th iteration, or when docs/STATUS changed since last run | Reads README, `docs/*.md` (`docs/security-model.md` first), `status/*.md` gaps, THREAT-MODEL.md, ADRs touched since the last audit, and the "Pinned by" column (does each test drive the production path?). For each security claim: the enforcing `file:line`, the test that pins it, and whether the claim is accurate, overstated, or stale. New overstated claims become hypotheses. |
| **Fixer** | `FIX_MODE: fix`, top of the fix queue | Writes the regression test first (must fail), then the smallest fix, then runs `make test`, `make lint` and the relevant `security/attacks` test. Reports the diff and the before/after test output verbatim. Never widens scope. |

The lead (you) never does an attacker's work inline beyond a ten-minute
spot check; the lead picks, briefs, verifies, records, and decides.

## Each iteration

1. **Read LEDGER.md.** Pick exactly one item, in this order:
   a. `FIX_MODE: fix` and the fix queue has an unblocked item → Fixer.
   b. A hypothesis (H-*) with status `open`, highest severity first, oldest
      first on ties → the attacker role its `role` column names, then the
      Verifier.
   c. A claim (C-*) with no pinned test → the matching attacker tries to
      break it; if it holds, write the pinning test anyway (that is the
      deliverable), status `pinned`.
   d. Nothing open → **fresh eyes**: `git log --since=<last iteration>
      --name-only` and list files touched; spawn the roles whose surfaces
      changed with the brief "find one new hypothesis in these files";
      otherwise run the Claims auditor.
2. **Write the plan in the ledger before spawning:** item, role(s), what
   result would confirm or refute it, which claim it attacks.
3. **Spawn.** Attacker(s) first, in parallel if independent; Verifier on
   each CONFIRMED candidate; never more than four agents.
4. **Classify:** `CONFIRMED` (executed test or verifier-agreed trace),
   `PLAUSIBLE` (traced, not executable here; name what would execute it),
   `REFUTED`, `DISCLOSED` (real but already in STATUS/docs as a known gap;
   record where), `HARNESS-BROKEN`.
5. **For CONFIRMED and PLAUSIBLE:**
   - Regression test `TestRT3_<Hxx>_<slug>` in `security/attacks/`
     (Linux-only ones under `//go:build linux`), skip-marked in `propose`
     mode. Reuse `security/attacks/util_test.go`.
   - Root cause with `file:line`, severity with the Pass 1 scale, the
     "STATUS accurate?" verdict, a fix sketch, and any new hypotheses.
   - Append the finding to the current pass section of RED-TEAM.md.
   - Open a GitHub issue with the `security` label for the row (search
     open issues first) and link it both ways (hard rule 8). A new
     Critical, or a High on B1, goes into a private security advisory
     (SECURITY.md) instead of a public issue until it is fixed.
   - If `FIX_MODE: fix`, add a fix-queue row.
6. **Update LEDGER.md** (status table, run log line, new hypotheses) and
   commit on `rt3-loop` with a message naming the item and verdict.
7. **Stop the loop and notify (PushNotification)** if any of these:
   - a **new Critical** (cross-tenant or platform compromise), or a new
     High on boundary B1 (tenant code ↔ platform). These need a design
     decision, so don't keep going;
   - HARNESS-BROKEN three iterations in a row;
   - every hypothesis resolved, every claim pinned, and the last two
     fresh-eyes sweeps produced nothing. Write a final summary at the top
     of LEDGER.md.

   Otherwise schedule the next iteration in 120–300 s (ScheduleWakeup).
   After a fresh-eyes sweep that found nothing, use 1800 s.

## Artifacts

`security/results/<UTC-stamp>-<item>/`: attacker and verifier reports
verbatim, the probe sources, test output, and the `pids` record. Keep
them out of git.

## Invariants (what the tests pin)

| ID | Invariant |
|---|---|
| I1 | A host's write to mgmt about an object, deployment, pool node or audit record succeeds only if the host holds that workload's lease (holder + epoch). |
| I2 | A public request never causes an object create, spawn, `ready`, or node mint except through `admit` under `public.create` with `maxObjects` and the per-tenant lane. |
| I3 | `getCaller()`, `caps()`, `principalOf` and every `actingFor`/`principal` written to audit derive only from netd's WhoIs blob on the ingress socket, never from a header, `request.cf`, or an op argument. |
| I4 | The host audit chain never silently loses a host-source record; overflow is visible in-chain, and `VerifyChain` distinguishes "dropped, attributed" from "tampered". |
| I5 | Every tenant-controlled or internet-controlled string is rendered by `tw` with control characters escaped. |
| I6 | A single client address cannot close a public listener to everyone; connection and request limits are per-IP as well as per-node. |
| I7 | A public name is owned by one tenant per tailnet for the life of the tenant; no other tenant can serve it. |
| I8 | Privileged helpers (vmhelper, hostapi, netd ctl) validate every field of every message from a less-trusted party and fail one session, never the process. |
| I9 | Every identity header tailworks itself sets is also stripped from clients, by prefix, on every ingress path (tailnet, public, Service, egress). |
| I10 | Prior red-team regression tests (`security/attacks/rt0*`) stay green un-skipped. |
