Last update: 2026-11-21 22:40 PT. Worker internet egress, on by default (#161, ADR-049).

# Worker internet egress (#161)

Ross decided the direction on #161: Workers have internet egress **on by
default**, with a setting to turn it off. This branch records that in an
ADR and builds it.

## What's built

- **ADR** `docs/design/adr/049-worker-internet-egress.md`
  (ADR-049, Accepted; drafted as `ADR-worker-internet-egress` and numbered in this PR per AGENTS.md
  rule 5). Traffic leaves from the **host's network** through netd, not an
  exit node or gateway; never as the object's tailnet identity; the source
  address is per host. Covers DNS (what the host resolver sees), audit,
  the shared schema, the opt-out, and upgrade behaviour.
- **netd** (`netd/internet.go`, `netd/egress.go`, one field in
  `netd/registry.go`): `Config.Internet *InternetConfig` (Dial, Resolve,
  HostAddrs, per-object Policy, OnFlow). A destination that isn't a
  tailnet destination (a non-tailnet IP literal, or a name not in the
  netmap, not a Service name and not tailnet-shaped) takes the internet
  path. Policy and allowlist are checked before DNS; the name is resolved
  at dial time, every answer is checked, and the checked address is
  dialled. Nil `Internet` keeps the old behaviour exactly.
- **SSRF guard** `netd/publicip` (new package, shared with
  `netd/sbxnet`, which now uses it): loopback/unspecified, RFC 1918, ULA,
  link-local (metadata), multicast/broadcast, CGNAT 100.64/10 (incl.
  100.100.100.100), the tailnet IPv6 range, reserved/benchmark/discard,
  IPv4-compatible, NAT64, 6to4, Teredo; netd adds the host's own addresses
  and ExtraForbidden. Special-use names (`.internal`, `.local`,
  `.localhost`, `.lan`, `.arpa`, `.test`, …) and numeric TLDs are refused
  before DNS.
- **hostd** (`hostd/internet.go`, one field each in `hostd/host.go` and
  `hostd/tenant.go`): `Config.WorkerInternet` (nil = off; tw-hostd, tw up
  and tw dev set `DirectInternet()`); the policy comes from the active
  deployment's `tailnet.egress.internet`; the first flow per object and
  destination is an `egress` audit record ("internet via this host's
  network (ip:port)"). Unknown objects fail closed.
- **Schema** `tailnet.egress.internet`: `true | false | [list]`
  (`hostd/bundle/internet.go`). Absent: Workers any public host,
  containers none (unchanged). `true`: both any public host. `false`/`[]`:
  neither. A list: both only those.
- **Flags/feature:** `tw-hostd -worker-internet=direct|off` (hostdrun
  `WorkerInternet`). Host feature `worker-internet`, required only by the
  boolean form (older hosts can't parse it).
- **Docs:** README, COMPAT (fetch and enableInternet rows), recipes (Call
  an internet API), tailworks-jsonc (new `tailnet.egress.internet`
  section), errors (`internet_egress_unavailable` = disabled by config;
  `forbidden_destination`, `peer_not_visible`), security-model (Egress),
  security/THREAT-MODEL.md (B11, attacker 2, §5), security/LEDGER.md
  (claim C-161, pinned), troubleshooting, wrangler.md, containers.md,
  upgrading.md, RFC (§4.8 revised + as-built row), ADR-019 rule 1 amended,
  skills, hello template README, JS error titles.

## Tests

New: `netd:TestInternetEgress` (real workerd and netd; allowed fetch,
public IP literal, opt-out, allowlist, every blocked range as DNS answer
and as literal, special names never resolved, mixed-answer and flipping
DNS rebinding, tailnet path unchanged, Node CONNECT path),
`hostd:TestWorkerInternetEgress` (hello template `fetch()` end to end:
default on + audit record, `false`, allowlist, guard),
`publicip.TestBlocked/TestSpecialName/TestAllowlist`,
`bundle.TestInternetEgress`, `features.TestRequiredWorkerInternet`,
schema cases. Hermetic: Resolve maps names to 203.0.113.0/24 and Dial
sends those to a local server, failing on any other address; the guard is
never relaxed. Mutation check: disabling `publicip.Blocked` in
`internetBlocked` fails four `TestInternetEgress` subtests.

Commands run (results below as they finish):

- `go test ./netd/... -count=1`: PASS.
- `go test ./hostd/ -run TestWorkerInternetEgress -count=1`: PASS.
- `go test ./schema/ ./hostd/bundle/ ./internal/features/`: PASS.
- `make lint`: PASS (`check-changes: ok (no upgrade-relevant paths changed)`,
  so `make e2e-upgrade` was not required and not run).
- `make test` (local; both mbp slots busy): PASS, before and after merging
  origin/master (ac62faa).
- `make templates-typecheck`: PASS (7 templates). `make test-sdk`: PASS.
- `CI=1 go test ./docs/ -run TestADRIndex` (after numbering ADR-049): PASS.
- Not run: credential/tailnet/Docker/KVM suites (`e2e-real`,
  `e2e-container`, `e2e-vm`, …).

## Decisions taken where the direction didn't settle it (conservative)

- Containers keep "absent = none"; only `true` widens them.
- `tw dev` gets internet egress too (as on a host and in `wrangler dev`);
  tw itself still makes no request under `tw dev`.
- Host resolver for internet names (leak documented); no per-tenant
  resolver yet.
- The boolean form needs the `worker-internet` host feature; `[]` is the
  mixed-fleet way to say off.
- `tw up` has no switch of its own for `-worker-internet`; it uses the
  default (`direct`).

## Gaps / next

- Per-tenant DNS resolver (DoH) behind `InternetConfig.Resolve`.
- A `tw up` config key for `-worker-internet=off`.
- Internet egress covers HTTP(S) `fetch()` and the Node tier's CONNECT
  tunnels; workerd's raw `connect()` is refused at the egress handler as
  before.
