Last update: 2026-11-24 PT. README: "What you should know before you run this" (#322).

# README: isolation, durability and identity up front

- New README section above "Compatibility at a glance" with three
  statements and links: isolation (process tier is not a VM boundary;
  microVM tier for code you don't trust), durability (Durable Object
  storage and `fs`-backend R2 bytes are on one host and start empty after
  a move; KV and D1 are journaled in mgmt), identity (WireGuard and WhoIs,
  never tokens; tailworks doesn't use the machine's own Tailscale).
- Differs from the issue's wording where the docs say otherwise: the issue
  said KV, D1 and R2 state all live on one host. `docs/COMPAT.md` (KV, D1,
  R2 rows) and `docs/r2.md` say KV and D1 survive a move and R2 keeps its
  index but not its `fs` bytes, so the README says that. The issue also
  offered containers as an isolation option; `ctx.container` runs an app's
  containers and is not a tier for the Worker, so only the microVM tier is
  named.

## Tests

- `make lint`: ok. `go test -count=1 ./docs/...`: ok. `make changes-check`: ok.
- Docs-only change: no other suites apply.
