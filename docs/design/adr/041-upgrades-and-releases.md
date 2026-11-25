# ADR-041: One release train, declared skew, expand/contract state, tested upgrades

**Date:** 2026-11-15
**Status:** Accepted (2026-11-16). Built, except where "Not built" says
otherwise (first shipped in tailworks 0.2.0); results in
`status/design-component-upgrade-release-process.md`.
**Updated 2026-11-25 (issue #486):** the CLI spellings moved under
`tw upgrade`: `tw upgrade fleet` (was `--fleet`), `tw upgrade fleet
status|pause|resume|abort`, `tw upgrade import` and `tw upgrade releases`
(were `release import|ls`), and the new `tw upgrade rollback`. The old
spellings stay hidden for one release.

## Context

The RFC (§10 "Upgrade order") says each component supports N−1 of its
neighbours. It also says mgmt rolls with "versioned API and store schema"
and that guests and workerd move blue/green. ADR-020 lists `upgrade --plan`
and `host drain` as day-2 verbs. None of this is built. What master does
today (2026-11-15, 46ff0c3):

- **Version identity.**
  - Only `cmd/tw` has a `main.version`. `release.sh` passes
    `-X main.version` to the other binaries, but they have no such variable,
    so the flag does nothing there. `tw-vz` gets no version at all.
  - `tw-mgmt` learns its version from a `-version` flag.
- **Skew detection.** Hosts, guests and mgmt replicas report no version.
  The only checked protocol is hostd ↔ vmhelper (`helperapi.Version = 2`),
  and that check is strict equality.
- **mgmt schema.** `migrate()` runs on every open and has three parts:
  `CREATE TABLE IF NOT EXISTS`, a hand-kept `addColumns` list, and
  `addIndexes`. There is no schema version, so nothing stops an old binary
  from opening a newer database, and nothing guards a downgrade.
- **Persisted formats** carry no version marker. That covers `objects.json`,
  netd `object.json`, `tw.json`, and (on branches) the R2 index journal.
- **hostd restart** stops every workload and VM, then rebuilds them from mgmt
  assignments and saved node state. Node identity survives the restart:
  nodes rejoin and nothing re-registers.
- **Missing verbs.** There is no `tw upgrade`. There is no graceful drain:
  `host rm --drain` kills the host's objects and deletes the host. There is
  no `tw-mgmt migrate` either (now built: `--check` and `--apply`, §3).
- **`install.sh`** swaps `bin/` and `share/` but doesn't restart anything,
  and it deletes the previous copy. The root-owned vmhelper changes only when
  `sudo tw host setup --microvm` is re-run.
- **workerd pin.** It is pinned twice: in `package-lock.json` and by hand in
  `cmd/tw/doctor.go`. The guest image carries its own copy, and nothing
  checks it against the host's.
- **Platform runtime.** `platform.js` and `__tw_rt.js` are embedded in
  hostd, but `@tailworks/*` client code is frozen into each bundle at deploy time.
  Upgrading a host therefore changes the runtime under old bundles.
- **Releases.** A `v*` tag builds three tarballs into a draft GitHub release,
  and a maintainer publishes it. Nothing installs the previous release and
  upgrades from it.

Work in flight makes this urgent. Each of these adds state or a contract that
an upgrade must carry:

- KV (#15), R2 (#16) and D1 (#17) add journal formats, which are permanent
  because the local state is a replay of the journal.
- Queues (#18) adds mgmt tables.
- Observability (#20) adds telemetry grants, host API routes and a new
  `tw logs` default.
- Workflows (#24) bumps workerd and keeps instances in upstream's
  experimental on-disk format.
- Agent DX (#25) adds a stable `--json` schema and version-stamped skills.
- Locks (#26) build fencing tokens from lease epochs.
- do-split (PR #11) changes how DO IDs are derived.

The per-branch list is in the status file.

## Decision

### 1. One release train, one version

- **Every artifact in a tarball shares one SemVer version.** That includes
  `tw`, `tw-mgmt`, `tw-hostd`, `tw-vmhelper`, `tw-vz`, the
  guest image (rootfs with `tw-guestd` and workerd, plus the kernel), the
  embedded platform JS, the admin UI, the create-tailworks templates and the
  `@tailworks/*` npm packages.
- No component is released on its own. A component's version is the
  release's version.
- **`internal/version`** is the single place a binary reads its version,
  commit, workerd pin and the protocol and schema versions it speaks. It is
  set by `-ldflags -X` in `release.sh` and the Makefile, for every binary
  including `tw-vz`.
  - The `-version` flag on `tw-mgmt` goes away.
  - The hand-kept `workerdPin` in `doctor.go` is generated from
    `package-lock.json`.
- **`release.json`** gains `protocols` (`push`, `guest`, `helper`, `rest`),
  `schema` (the newest mgmt migration ID) and `formats` (the newest version of
  each persisted format). The guest `manifest.json` gains `tw_version`.

### 2. Each contract has its own version and its own rule

The release version answers "what is installed". Compatibility is decided
per contract:

| Contract | Version carrier | Rule |
|---|---|---|
| mgmt REST `/v1` (CLI, hostd, UI) | path + spec | Additive only within `/v1`. New fields optional, new enum values tolerated by clients (generated clients must not reject unknown values). Removal needs `/v2`. |
| mgmt → hostd push API | `push` integer + host `features` | Same additive rule; mgmt sends a message only to hosts whose `features` include it. |
| hostd ↔ guestd (vsock) | `hello` gains `proto` + `version` | hostd and its guest image ship together (§5), so skew only occurs mid-restart; guestd answers `unknown op` and hostd treats that as "feature absent". |
| hostd ↔ vmhelper | `helperapi.Version` + `Features` | Helper serves its version and the one before it; hostd requires a minimum, not equality (`vm_linux.go` check becomes a range). |
| hostd ↔ tw-vz | `vzapi` gains a version in the stdin config | Shipped in the same tarball; refuse on mismatch with a "reinstall" hint. |
| hostd runtime ↔ deployed bundle | deployment records the building CLI's version (`builtBy`) | hostd must run bundles built by the current and the previous minor. Older bundles keep running but `tw ls` flags them `rebuild_recommended`. |
| mgmt store schema | `schema_migrations` table (§3) | Expand in N, contract no earlier than N+1. |
| Persisted formats (journals, `objects.json`, `object.json`, `tw.json`, queue bodies) | a version field in every record or file | Read all supported versions; write a new version only one release after readers exist (§4). |
| CLI `--json` (`schema/cli.schema.json`) | `$id` carries a schema major | Additive within a major; a breaking change is a new major and a release-note **Breaking** item. |
| workerd + `compatibility_date` | pin in `package-lock.json` | One pin per minor release; patch releases don't bump it except for security fixes (§6). |

### 3. mgmt schema: numbered, expand/contract, downgrade-guarded

- **Migration files.** Migrations become one file each in `mgmt/migrations/`,
  named `<UTC yyyymmddhhmm>_<slug>.sql`, with `.pg.sql` and `.sqlite.sql`
  variants where they differ.
  - Timestamps plus slugs don't collide across parallel branches (AGENTS.md
    rule 4).
  - Migrations apply in ID order. Each must be valid on its own, so a branch
    that merges "late" with an older timestamp still applies.
- **Baseline.** The current DDL, `addColumns` and `addIndexes` are frozen as
  migration `000000000000_baseline`. That migration is idempotent, so
  existing databases adopt it unchanged. No further edits go into
  `schemaCommon`.
- **Bookkeeping.**
  - `schema_migrations(id, applied_at, applied_by_version)` records each
    migration applied.
  - `schema_meta(min_binary_version)` records the oldest mgmt binary that may
    open the database.
  - Postgres keeps `pg_advisory_lock(7464)` while migrating.
- **Expand/contract.**
  - Release N may only add tables, nullable or defaulted columns, and
    indexes.
  - Dropping, renaming, or tightening a constraint is a contract migration.
    It ships in N+1 or later, and only once every replica reports ≥ N (§7).
  - A contract migration raises `min_binary_version`.
- **Downgrade guard.**
  - A binary that finds migration IDs it doesn't know (applied by a newer
    tw-mgmt) still opens the store: those are expand migrations, which an
    N−1 binary tolerates by construction. `/v1/status` lists them under
    `versions.schema.ahead`. (The draft had such a binary serve reads only;
    the minimum-version rule below already covers the unsafe case, and a
    read-only mode would have touched every write path.)
  - A binary older than `min_binary_version` refuses to start, with
    `version_skew`.
  - **Contract migrations wait.** Opening the store applies expand
    migrations at once but defers contract ones while a replica on an older
    version is live (the `replicas` table). An upgrade run's `contract` step,
    or `tw-mgmt migrate --apply`, applies them once every replica runs the
    new version.
  - Rolling back means restoring the pre-upgrade backup (§8), never
    down-migrating.
- **`tw-mgmt migrate --check`** prints the pending migrations and whether
  any is a contract migration. `--apply` runs them, contract ones included,
  without serving. `tw-mgmt restore-fence` fences a restored store (§8).
  `tw upgrade --plan` (§8) calls the check.
- **Tenant D1 migrations are not affected.** They stay inside the tenant's
  store, use wrangler's `d1_migrations` table, and are never called "schema
  migrations" in release notes.

### 4. Persisted formats: versioned, read-old/write-new, fixtures forever

- **Every persisted record or file carries a version.**
  - The KV wire format already has a version byte (`0x01`), and D1 entries
    have `v:1`.
  - **R2 index entries must gain `v` before #16 merges.**
  - `objects.json`, netd `object.json`, `tw.json`, `approvals.json` and
    queue message rows gain `v` as well; a missing `v` means version 0.
- **Journals are permanent.** Compaction keeps old entries, so readers keep
  every version they have ever written. The alternative is a rewrite
  migration that re-journals a store at the new version and records
  `format_min` on the store row.
- **Two-step format changes.** Release N ships a reader for v2 and still
  writes v1. Release N+1 writes v2. This keeps mixed hosts, and a rollback to
  N, safe.
- **Fixtures.** Each format gets a golden fixture under `testdata/formats/`,
  generated by the release that introduced the version and never edited
  afterwards. A unit test decodes every fixture.
  - The Go and JS KV encoders are both tested against the same fixtures.
- **Upstream-owned formats** are covered by the workerd bump gate (§6):
  workerd DO SQLite, and the experimental Workflows engine's
  `miniflare-workflows-*` namespaces.

### 5. Host upgrades: same-version bundle, cordon, bounce or drain

- **hostd always runs the guest image from its own `share/tailworks/vm`.** The
  guest therefore moves with hostd and never on its own; workerd follows from
  §6.
- **Snapshots never cross versions.** ADR-030 bases are already rebuilt at
  every hostd start. From now on, the snapshot key also includes the guest
  `manifest.json` hash, so a base snapshot can never be restored under a
  different guest.
- **New verbs:**
  - `tw host cordon <id>` and `tw host uncordon <id>` mark a host
    unschedulable, which is the "draining state" ADR-033 lacks. Placement
    skips cordoned hosts.
  - `tw host drain <id>` cordons the host, then moves what it can:
    - Stateless replicated workloads: start the replacement elsewhere first,
      then stop the local one.
    - Stateful namespaces whose storage is all mgmt-backed (platform stores,
      queues, secrets): hand the lease over at the next epoch, as in RFC §10
      "Drain".
    - Workloads with **host-local state** (process-tier `do/`, `do.img`, R2
      `fs` blobs, `approvals.json`, request logs) are refused unless
      `--allow-local-state-loss` is given. On a single-host MVP namespace
      there is nowhere to move them, so the answer is a bounce, not a drain.
  - **Bounce** (the default when an upgrade run (`tw upgrade fleet`)
    reaches a host that can't drain):
    1. Install the new tarball side by side.
    2. Upgrade vmhelper first. It speaks old and new (§2), and the VMs restart
       with hostd anyway.
    3. Restart hostd, which rejoins nodes from saved state.
    4. Report the measured RTO against the RFC's "netd crash/upgrade" row
       (≤ 1 min).
- **Order across hosts:** canary one host (or one tenant pool), then the
  rest one at a time, with each host waiting for `syncAssignments` to settle
  and its objects to rejoin.
- **Later (v1, not this ADR's scope):** hostd adopts running workload
  processes and VMs across its own restart, which would make a host upgrade
  zero-downtime. It needs VMs to outlive the helper connection, and that
  changes ADR-024's privilege model, so it gets its own ADR.

### 6. workerd and compatibility dates

- **One pin per minor release,** in `package-lock.json`. Everything else is
  derived from it: `doctor`, the guest image and `release.json`.
- **A bump is its own PR,** never folded into a feature branch. It must pass:
  - `TestRuntimeCompat` and `make test-sdk`, which run the Agents SDK,
    partyserver and MCP;
  - the persisted-state fixtures for upstream formats: a DO SQLite database
    and a Workflows instance, both written by the previous pin, must read
    back unchanged;
  - an update to `docs/COMPAT.md`.

  The workerd bump on `workflows-approvals` is extracted under this rule
  (AGENTS.md rule 6: land the shared change first).
- **A tenant's `compatibility_date` is never changed by tailworks.**
  - At deploy, mgmt refuses a date newer than the pinned workerd supports
    (`compatibility_date_unsupported`).
  - Platform-owned dates (`PlatformCompatDate`, the deny worker, VM warm-up)
    move only with a workerd bump, and the release notes say so.
  - Auto-added flags such as observability's `nodejs_als` are listed in the
    release notes.

### 7. Skew policy and enforcement

- **Policy (N = the mgmt release):**
  - **mgmt replicas:** N and N−1 may run together during a roll. No
    contract migration runs while any replica is below N.
  - **hostd:** N−1 ≤ hostd ≤ N. A host newer than mgmt is refused.
    Hosts upgrade after mgmt.
  - **CLI:** any version ≥ N−1. An older CLI is warned on every call;
    a newer CLI is warned and may not use features mgmt lacks.
  - **Bundles:** built by N or N−1 run unchanged (§2).
  - **Guest image, platform JS, admin UI:** always equal to the hostd or mgmt
    that ships them.
- **Enforcement:**
  - `HostRegistration` and `HostStatus` gain `version`, `protocols` and
    `features` (and `os`, for picking its tarball). mgmt rejects an
    out-of-window host when it registers (`409 version_skew`). Heartbeats
    are never refused: a host that is already registered keeps its leases
    and its running workloads, placement skips it (`version skew: …` in the
    decision), and `/v1/status` lists it under `versions.skewed`.
  - The `Replica` record gains `version`. `/v1/status` reports
    `versions: {mgmt: [...], hosts: {v: count}}`.
  - The CLI sends `User-Agent: tw/<v>`, and mgmt answers with
    `Tw-Version`. On a mismatch outside the window the CLI warns on stderr,
    once. It doesn't warn in `--json` mode, whose stderr carries only the
    error line, and it never fails a command over skew: mgmt's own checks
    refuse what is unsafe.
- **Features gate new work, not version numbers.**
  - Each new binding kind or host-side capability declares a feature string
    (`internal/features`). Every registered platform store kind is one
    automatically: `d1`, `kv` and `r2` today, and `queues` when it lands.
    Static ones: `telemetry`, plus `self-upgrade` from a host started with
    `-self-upgrade`. In-flight work adds `workflows`, `approvals` and
    `do-split`.
  - Hosts that predate version reporting offer no features. A deployment
    that needs one never lands on them, even though their version (0.1.x) is
    within the window.
  - Placement (ADR-033 eligibility) puts a deployment only on hosts that
    advertise every feature it needs. If no host does, the deploy fails with
    `feature_unavailable: queues (hosts h1,h2 run 0.3.x; upgrade them)`.
  - This is what makes the in-flight features safe to merge in any order
    and to roll out host by host.

### 8. `tw upgrade`

- **Homelab (`tw up`, ADR-021): `tw upgrade [vX.Y.Z] [--plan] [--yes]`.**
  1. Resolve and download the release (or take `--from`), extract it, and
     verify the tarball's `SHA256SUMS.sig` against the release keys pinned
     in the binary (`internal/releasesig`, the verifier tw-hostd's
     self-upgrade uses) before anything in it runs, then the platform.
     An unsigned or badly signed release is refused unless
     `--allow-unsigned` (issue #98).
  2. **Plan.** Print the from and to versions, the release-note **Breaking**
     and **Upgrade action** items between them, the pending mgmt migrations
     (flagging contract ones), any workerd or compat-date change, and
     whether vmhelper needs `sudo`.
  3. **Back up.** SQLite `VACUUM INTO`, plus a copy of `custody/` and
     `issuer.key`, as one timestamped bundle under `~/.tailworks/backups/`. The
     database and custody are only valid together: platform store, queue
     and secret data is sealed by custody keys.
  4. Stage the release; `install.sh` keeps the previous copy as `bin.prev`
     and `share.prev` instead of deleting it.
  5. Restart the supervisor (`systemctl --user`, `launchctl`, or in place),
     then health-check: mgmt `/v1/status`, host heartbeat, every assigned
     workload `ready`.
  6. **Roll back automatically** if the health check fails *and* no
     contract migration ran: swap `.prev` back, restore the backup, and
     restart. Otherwise stop and print the restore command.
- **Skipping versions.** `tw upgrade` walks through every intermediate
  minor that has a contract migration, so a skip never jumps a contract
  step.
- **Release catalog in mgmt.** mgmt keeps a catalog of releases, so
  planning needs no network access.
  - Each release embeds its catalog entries in `tw-mgmt`: its own
    `release.json` plus the assembled release notes (§9) for itself and
    every earlier release. Entries carry `upgrade` kinds, contract
    migrations, protocol and format versions, and the workerd pin.
  - A newer target is added with `tw upgrade import <tarball>…`, which
    verifies the tarball against `SHA256SUMS`, posts its
    `share/tailworks/catalog.json` (the `releases` table), and uploads
    `SHA256SUMS` and the tarball to mgmt's release directory (`-release-dir`,
    default `<state-dir>/releases`; an upload must match `SHA256SUMS`).
    Replicas that don't share that directory each need the import.
    `tw upgrade import <version>` downloads the release's `SHA256SUMS` and
    `SHA256SUMS.sig` instead, checks the signature against the pinned keys
    (there is no way past it), and imports the tarball of each platform
    the self-upgrading hosts run. Nothing fetches implicitly: `--plan` and
    a bare `tw upgrade fleet` plan from what the catalog has. Not built:
    opt-in `--release-source github`, which would have mgmt fetch new
    entries itself.
  - `GET /v1/releases` lists the catalog; `tw upgrade releases` shows it.
- **Production: a mgmt-side upgrade orchestrator.** The fleet upgrade runs
  in mgmt, not in the CLI, so it survives a laptop disconnecting and is
  visible to every operator.
  - `tw upgrade fleet vX.Y.Z --plan` asks mgmt for a plan
    (`POST /v1/upgrades?dryRun=true`). mgmt computes it from the catalog
    and the fleet's reported versions and features:
    1. Check the store backup (point-in-time recovery is on).
    2. Roll mgmt replicas.
    3. Run contract migrations, if any; they need every replica at N.
    4. Canary host, then the remaining hosts: cordon, drain or bounce,
       uncordon.

    The plan flags hosts that lack a feature some deployment needs, and
    every intermediate minor that has a contract migration.
  - `tw upgrade fleet vX.Y.Z` creates an **upgrade run**
    (`POST /v1/upgrades`), a durable record in the store: the plan, the
    current step, and per-host state. Its steps are driven by the existing
    stateless-replica model (ADR-011): whichever replica holds the run's
    lease drives it, and another replica takes over if that one dies.
  - **Steps that mgmt can't do itself:**
    - **Hosts:** mgmt pushes `POST /v1/push/upgrade` with the release and
      artifact digest. hostd downloads the tarball from mgmt over the
      tailnet, verifies it, stages it, and asks its supervisor to restart
      it on the new version: the systemd unit, or `tw up`'s supervisor.
      The host reports back by heartbeat, with its new `version`.
    - **vmhelper** is root-owned. The run pauses on hosts whose helper
      needs an upgrade (`needs_operator: sudo tw host setup --microvm`)
      unless the host was set up with `--allow-helper-upgrade`, which
      installs a root-run updater limited to a release's helper binary.
    - **mgmt replicas** run under the operator's own deployment system, so
      the run waits until every replica reports the target version. It
      doesn't restart replicas itself.
  - **Gates between steps:** each host must settle (`syncAssignments` done,
    objects rejoined, no new device registrations) before the next host
    starts. The canary waits a configurable soak time. A failed gate pauses
    the run (`paused: <reason>`). `tw upgrade fleet status|pause|resume|abort`
    controls the run, and every transition is audited (AuditKinds
    `upgrade_start`, `upgrade_step`, `upgrade_pause`, `upgrade_abort`).
  - The homelab `tw upgrade` uses the same plan code, through the catalog
    of its embedded mgmt.
- **Restore must not reuse lease epochs.** Lock fencing tokens (#26) and the
  ADR-028 fences are built from epochs. Every restore, including the
  automatic rollback and a future SQLite→Postgres store copy (not built),
  raises each lease's epoch past anything handed out before the backup by
  writing a restore generation into `schema_meta`.

### 9. Release process

- **Versions.**
  - **Before 1.0:** `0.MINOR.PATCH`. A minor may break, with a migration
    path and a **Breaking** note. A patch fixes bugs only: no migrations,
    no format change, no workerd bump, no new feature string.
  - **From 1.0:** standard SemVer. Breaking changes require a major, and
    config and API are stable within one (RFC "Compatibility promise").
- **Branches.**
  - master is always releasable.
  - A minor release is a tag on master after `VERSION` is bumped.
  - A patch is cut from a `release-0.N` branch, created when the first patch
    is needed, by cherry-picking from master.
  - Before 1.0 only the latest minor gets patches; security fixes also go to
    N−1.
- **Release notes come from fragments,** not a shared CHANGELOG, so parallel
  branches don't collide (the same reason as `status/`).
  - Each branch adds `changes/<branch>.md` with front matter:
    - `kind`: `feature | fix | breaking | security`;
    - `upgrade`, a list drawn from `schema-expand`, `schema-contract`,
      `format`, `feature-string`, `protocol`, `workerd`, `compat-date`,
      `config-default`, `cli-json`.
  - The body is the user-facing text, plus any operator action.
  - `scripts/release-notes.sh <prev> <tag>` assembles them in sorted
    sections, and the release moves them to `changes/released/<tag>/`.
- **`make lint` enforces the fragments.** A branch that touches
  `mgmt/migrations/`, `api/openapi.yaml`, `schema/cli.schema.json`,
  `package-lock.json`'s workerd entry, `guest/inputs.sh`, a format fixture,
  or `helperapi.Version` must carry a fragment whose `upgrade` names that
  kind.
- **Release gates** in `release.yml`, ahead of the draft:
  1. `make lint test` and the hermetic e2e suites, as today.
  2. **Upgrade test** (new, `e2e/upgrade`):
     - Download the previous published release, `tw up`.
     - Deploy the fixture app, which exercises DO storage, a secret, KV, D1,
       R2, a queue with a pending message, a workflow waiting on approval, a
       cron and a public route. Each part is added as its feature merges.
     - Run `tw upgrade` to the candidate, then assert: data intact, nodes
       rejoined without re-registering (device count unchanged), old bundles
       still serving, the pending message delivered and the workflow
       resumable.
  3. **Skew test:** mgmt at the candidate with one hostd at the previous
     release, then the reverse, which mgmt must refuse.
  4. **Downgrade guard test:** the previous binary against the migrated
     store must fail with `version_skew`, not corrupt it.
  5. **microVM upgrade test** on an amd64 Linux KVM machine and in
     Lima tw-dev (arm64): the same flow on the microVM tier, including vmhelper
     protocol N−1 → N.
  6. **Headscale:** gates 2 and 3 also run against each of the two supported
     Headscale minor releases (RFC "Compatibility promise"), pinned in CI,
     as well as testcontrol. A new Headscale minor is added to the matrix,
     and the oldest dropped, in its own PR with a fragment.
- **Artifacts.**
  - Unchanged: three tarballs, `SHA256SUMS`, draft, maintainer publishes.
  - New: GitHub artifact attestations for the tarballs and `SHA256SUMS`.
    Nothing verifies them yet: `get.sh`, `install.sh`, `tw upgrade` and
    tw-hostd check the release signature (`SHA256SUMS.sig`) instead.
  - npm `@tailworks/*` packages are published at the same version from the same
    tag, after the GitHub release is published.
- **Deprecation.** A surface deprecated in N warns (a CLI warning, plus a
  `Deprecation` header on REST) and is removed in N+2 at the earliest, with a
  **Breaking** note.

## Merged features and work still in flight

Since this ADR was drafted, observability (#30), agent DX (#32), R2 (#35,
ADR-036), KV (#37, ADR-037), D1 (#33, ADR-038), Queues (#36, ADR-039),
Locks (#31) and Workflows (#34, ADR-040) merged. This branch retrofits them:

| Feature | Done here |
|---|---|
| KV (ADR-037) | Feature `kv` (a registered store kind). Golden fixtures for the wire format, decoded by both the Go and the JS reader; a newer version byte is refused with "written by a newer tw". |
| R2 (ADR-036) | **Index entries now carry `v:1`**, and a missing `v` reads as v1. The JS replay and `tw r2` refuse a newer `v`. The replay also refuses unknown ops, which it used to skip. Feature `r2`. |
| D1 (ADR-038) | Feature `d1`. The journal replay refuses a newer `v`; it used to skip it silently, which loses writes. Release notes say "D1 migrations" for tenant migrations. |
| Observability | Feature `telemetry`. |
| Queues (ADR-039) | Feature `queues` (static: queues are a mgmt broker, not a store kind), required by any bundle with producers or consumers. Its tables went into the baseline DDL before `mgmt/migrations/` existed, which is fine: the baseline is idempotent. **Still open:** a format version on stored `v8` bodies, since V8 serialization can change with workerd. |
| Workflows (ADR-040) | Its workerd bump (1.20261114.1) is in `internal/version.WorkerdPin`. Feature `workflows`, required by any bundle with wrangler `workflows`. **A Workflows app is now stateful** (`BundleStateful`): its instances live in the workload's DO storage on the host, so it is fenced and a drain leaves it in place. Before, a takeover or drain treated it as stateless and would have lost its instances. `approvals.json` records carry `"v":1`; a newer version isn't loaded. Approvals are still host-local and don't move with the lease. |
| Locks | `docs/locks.md` says the epoch-based tokens stay monotonic across a restore because of the restore fence. |
| Agent DX (ADR-035) | `upgrade`, `release` and `host cordon|uncordon|drain` are in `schema/cli.schema.json` and the CLI contract test. |

Still in flight, and what each must do before it merges:

| Branch / issue | Before merge |
|---|---|
| do-split PR #11 | Existing workloads keep today's ID derivation; only workloads that opt in with `"objects": "single-homed"` use the group-derived ID, so there is no ID migration. Feature `do-split`; the host-to-host request carries a version. |

## Phases and what is built

Built on this branch, with tests:

- **Phase 0:**
  - `internal/version` for every binary, with the workerd pin checked
    against `package-lock.json`.
  - Version, OS, protocols and features in host registration; version in
    `Replica`.
  - `schema_migrations`, `schema_meta` and the frozen baseline, plus
    `mgmt/migrations/`.
  - Version fields and golden fixtures for the persisted formats.
  - `changes/` fragments and `make changes-check`.
- **Phase 1:**
  - Skew enforcement and feature-gated placement.
  - Homelab `tw upgrade`: plan, backup, install, restart, health check,
    automatic rollback with a restore fence.
  - `install.sh --rollback`, with `.prev` kept, and `tw upgrade rollback`
    on top of it (the store goes back too when a contract migration ran).
  - `tw-mgmt migrate --check|--apply` and `restore-fence`.
  - The embedded catalog, `GET /v1/releases`, and `tw upgrade releases|import`.
  - `make e2e-upgrade`: the newest published release, built in a temporary
    worktree, upgraded to the candidate (also the downgrade check), wired
    into `release.yml`'s `gates` job.
- **Phase 2:**
  - `tw host cordon|uncordon|drain`.
  - vmhelper protocol ranges; guest `hello` and `tw-vz` protocol fields;
    base snapshots keyed by guest identity.
  - The fleet plan from mgmt.
- **Phase 3:**
  - Upgrade runs, `POST /v1/push/upgrade` and `tw-hostd -self-upgrade`.
  - `tw upgrade fleet status|pause|resume|abort`.
  - Restore-generation epochs (`RestoreFence`).

Not built:

- **Half of the Headscale gate (§9, gate 6).** The Headscale profile (ADR-015,
  #39) merged. `release.yml` now runs `make e2e-headscale` for each supported
  Headscale minor (a matrix). Only v0.29 is supported until v0.30
  (docs/headscale.md), so the matrix has one entry for now. Running
  `e2e-upgrade` itself against Headscale is not built.
- **The opt-in root vmhelper updater** (open question 1). A run's plan warns
  when a host's helper protocol changes.
- **The microVM upgrade gate (gate 5).** It needs a KVM runner in CI.
  `make e2e-upgrade` covers the process tier.
- **GitHub artifact attestations** and `--release-source github`.
- **A stale-skills check in `tw doctor`.** `tw upgrade` reminds you to
  re-run `tw agent setup`.
- **The schema major carried in `$id` of `schema/cli.schema.json`.** It is
  `internal/version.CLIJSONMajor`, reported in `protocols.cliJSON`; the
  file's URL is unchanged.
- **Workload adoption across a hostd restart.** It needs its own ADR.

## Consequences

- **Upgrades get a trust floor.** An operator can always see what is
  running (`/v1/status`), what an upgrade will do (`--plan`) and how to undo
  it (backup plus `.prev`).
- **Features can merge in any order.** Each one is invisible to placement
  until its hosts advertise it.
- **Contract migrations, format rewrites and REST removals cost a release
  cycle.** That is deliberate.
- **Readers stay forever.** Journal readers and golden fixtures accumulate.
  The rewrite migration (§4) is the escape hatch.
- **Host upgrades on single-host namespaces cost a bounce.** They take about
  a minute of unavailability until hostd can adopt running workloads
  (phase 3). The release notes and `--plan` say so.

## Resolved questions

1. **Who runs the fleet upgrade:** mgmt, as a durable upgrade run driven by
   whichever replica holds its lease (§8). The CLI only asks for a plan and
   controls the run.
2. **Release catalog:** yes. mgmt embeds the catalog up to its own release,
   and newer releases are imported (or fetched, if opted in), so planning
   works offline (§8).
3. **Headscale:** the upgrade and skew gates run against both supported
   Headscale minors as well as testcontrol (§9, gate 6). The Headscale e2e
   runs per supported minor in `release.yml`; the upgrade test itself still
   uses testcontrol.

## Open questions

1. The opt-in vmhelper updater runs as root, outside the ADR-024 privilege
   model. Should it exist at all, or should a microVM host upgrade always
   stop for an operator's `sudo`?
2. Hosts download tarballs from mgmt, so mgmt stores every imported release
   for each arch. How many releases should it keep: the current and target
   only, or one more for rollback?
