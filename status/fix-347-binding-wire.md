Last update: 2026-11-24. A tenant `fetch()` can no longer forge a service binding call (#347, ledger H-binding-wire); the raw-socket path is documented as a limit.

# fix-347-binding-wire: the `Tailworks-Binding` header is hostd's to write

## What

- The red team's fresh-eyes pass (2026-11-23, master `27fd3de4`) found that
  `Tailworks-Binding`, the header a service binding call to a named
  entrypoint or RPC method carries between hosts (ADR-053), was
  tenant-settable. A plain `fetch()` with the header, from a workload or
  object with a grant to a sibling workload of its tenant, ran the
  sibling's named entrypoint or RPC method without a declared `services`
  binding.
- The header is now removed from tenant requests in two places:
  - the platform worker: `objectEgress` and `WorkloadEgress.fetch`
    (`hostd/gen/js/platform.js`), `bindingEgress` (`hostd/gen/js/svc.js`);
  - hostd: `takeBinding` (`hostd/servicebind.go`, called by the workload
    and facet egress handlers for every request) and `nodeEgress`
    (`hostd/node.go`, the Node tier's HTTP proxy).
  Only `routeBinding` writes it, for a call the platform marked.
- The callee side (`bindingIngress`) is unchanged.

## What it does not fix

- Found while fixing, and run: tenant code that writes HTTP itself on a
  raw socket to the target's port 80 (`connect()`, ADR-052; a Node app's
  CONNECT tunnel) still sets the header, and the callee honours it. hostd
  can't strip bytes it doesn't parse, and the callee authorizes the
  calling node, not the code that wrote the header.
- That matches ADR-053 §8 (the grant and the tenant certificate are the
  authorization), so it is documented, not changed: ADR-053 §6 and §8,
  docs/wrangler.md (service bindings), docs/security-model.md (Known gaps),
  `security/THREAT-MODEL.md` B3. The two code comments that said only
  hostd produces a binding call are corrected.
- Closing it needs a wire header the callee can authenticate (signed by
  the calling hostd, or a token mgmt issues at resolution). That is a new
  protocol field and a design decision. Not done here.

## Tests

- `hostd.TestServiceBindings`: new subtests `TenantWireHeaderStripped`,
  `TenantWireHeaderStrippedFromFacetObject` and
  `RawSocketStillCarriesWireHeader`, on fakemgmt (same host and another
  host) and the real mgmt (same host). With the fix reverted the first two
  fail (`entry:Admin`; the RPC probe returns `ok:true value:5`); the third
  passes before and after (it pins the documented limit).
- `hostd.TestEgressDropsTenantBindingWire` (new,
  `hostd/servicebind_internal_test.go`): hostd's workload egress and Node
  proxy handlers, and `takeBinding`, without the platform worker. Fails
  with the hostd change reverted.
- `gen.TestGeneratedWrapperProbe` (`ForgedEgressHeadersIgnored`,
  `WorkloadEgressIsWorkload`): the platform worker alone. Fails with the
  JavaScript change reverted.

## Commands run (darwin/arm64, go1.27.4, after branching from master `a7a7393c`)

- `go test -run TestServiceBindings ./hostd/` with the production change
  reverted: FAIL as intended (`TenantWireHeaderStripped` and
  `TenantWireHeaderStrippedFromFacetObject`, same host and other host). With
  the change: PASS, fakemgmt and the real mgmt (44 s).
- `go test -run TestEgressDropsTenantBindingWire ./hostd/`: FAIL with
  `hostd/servicebind.go` and `hostd/node.go` reverted, PASS with them.
- `go test -run TestGeneratedWrapperProbe ./hostd/gen/`: FAIL with the two
  JavaScript files reverted, PASS with them.
- `make lint`: ok (`check-changes: ok`, `changes/fix-347-binding-wire.md`).
- `make test`: PASS (every Go package with `-race`, and the JS tests).
- `make e2e-local`: PASS. `make test-node`: PASS (the Node tier's proxy is
  one of the changed handlers). `make test-sdk`: PASS.

- After merging `origin/master` (`4fb7e0f2`): `make lint` ok, `make test`
  PASS.

Not run: `make e2e-compat` (the table in AGENTS.md lists it for changes to
the runtime wrapper), `e2e-real`, `e2e-public`: they need a real tailnet
and its credentials. `e2e-vm`, `e2e-vz`: need KVM or the Mac's
virtualization entitlement. The microVM tier serves the same egress
handlers as the process tier (ADR-052 §9).
