# fix-614-compat-agents: nightly e2e-compat (#614)

The 2026-11-25 nightly was the first to deploy the upstream SDK examples
(#325); the night before ran 30 projects. Findings:

- **ag-mcp-worker-auth (deployed) FAIL, GET / timeout.** Two causes:
  1. The compat policy granted `tag:ttest → tag:tw-wl-compat` only
     `tcp:80`, so the client's connections to the workload's tailnet
     HTTPS listener (:443, #360) were dropped. Fixed: the grant is
     `tcp:80, tcp:443` (the run added it to tailnet 1's policy, within its
     approved snippet).
  2. Tailnet 1 issues no new ts.net certificates in time: with the grant in
     place, the LocalAPI cert request for the workload node, the mgmt node
     and even the plain tsnet client node's own name all time out (4 min,
     5 min, 8 min waits). Environment, as in
     status/release-rehearsal-fixes.md (Let's Encrypt limits per
     registered domain; every run mints fresh `*.tail7906d5.ts.net` names).
     The deployed run now waits up to 8 min for a TLS handshake before
     probing (`waitTailnetCert`) and fails with a message that says so.
     Not verified passing: needs certificate issuance to work on tailnet 1.
- **ag-cross-domain, ag-mcp-elicitation XPASS (deployed).** Since #359 an
  agent class not in `tailnet.objects` has no node and needs no create
  admission, so the imported env works deployed. This branch first added
  a `tw dev`-only expectation (they still failed under `tw dev`), but #642
  (015f2f30) landed meanwhile and makes unlisted classes plain under
  `tw dev` too, flipping both to expected pass. Merged master's version;
  nothing of that left here.
- **ag-playground FAIL (6 of 9 demos, 500s).** Not reproduced: passed in
  3 of 3 runs here (alone, with the other three, and in a full
  `make e2e-compat`). Left as is.

## Tests run

- `TW_COMPAT_ONLY='ag-(playground|mcp-worker-auth|cross-domain|mcp-elicitation)$' make e2e-compat` (master): reproduced worker-auth FAIL + 2 XPASS; playground PASS.
- `make e2e-compat` (full, grant fix): FAIL 1 (worker-auth, cert), PASS 70, XFAIL 7, XPASS 2 (built before ExpectDev).
- `TW_COMPAT_ONLY='ag-mcp-worker-auth$' make e2e-compat` x2: no certificate in 5 min / client node's own cert 4 min timeout.
- `TW_COMPAT_ONLY='ag-(playground|cross-domain|mcp-elicitation)$' make e2e-compat`: 3 PASS.
- `go test -tags e2ecompat ./e2e/compat/` unit tests: pass.
