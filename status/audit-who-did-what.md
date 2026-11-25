# audit-who-did-what (#406)

User-facing audit copy leads with what the trail is for (who did what,
when and why); chain verification speaks up only when it fails.

## Done

1. `tw audit` short help: "who did what, when and why"; long help names
   it the tamper-evident audit trail. `make cli-docs` run. The MCP `audit`
   tool description says the same. The chain/signature wording in
   docs/security-model.md, the RFC and the API spec is unchanged.
2. `tw audit` footer: `N records · chain verified` when every chain
   verifies (per-chain detail only for a retention-pruned chain);
   `chain NOT VERIFIED: N records · <per chain>` and `audit_unverified`
   when one fails, as before. `--json` is unchanged.
3. Console: the overview's audit tile and chain list appear only when
   verification fails; the object page drops the "verified" badge and
   chain list unless the page's chains fail; the audit page opens with
   "Who did what, when and why…", badges the records only when NOT
   VERIFIED, and its integrity card sits below the records (above them
   when a chain fails). docs/ADMIN-UI.md updated to match.
4. `GET /v1/audit/verification` and `audit_unverified` unchanged.

## Not done: AuditKind review (item 4)

Reviewed, nothing removed. Several kinds read as plumbing to a `tw audit`
reader (`secret_release`, `telemetry_release`, `container_reserve`,
`container_release`, `volume_key`, `transfer`, `revoke`, `remint`,
`credential`, `obo_root`, `image_pin`, `tenant_purge_host`,
`release_artifact`, `upgrade_step`), but each records access to key
material, a credential or a host-side side effect, and several are
written through the audit outbox (`m.intend`, mgmt/auditoutbox.go), so
they are load-bearing. Moving any to logs is a security and protocol
(AuditKind enum) decision for the owner; a lower-risk option is a default
`tw audit` view that hides plumbing kinds unless `--kind` or `--all` asks.

## Tests

- `go test ./mgmt/ui/ ./mgmt/ ./cmd/tw/` targeted runs: pass (new
  `TestAuditVerificationQuietWhenOK`, mgmt/ui/audit_quiet_test.go).
- `make lint`: pass (local).
- `make test` (offloaded to mbp with mbp-test.sh, after merging origin/master): pass.
- `make changes-check`: fragment changes/audit-who-did-what.md
  (`config-default`: CLI output).
