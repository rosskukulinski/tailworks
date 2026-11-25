---
kind: feature
upgrade: [config-default]
---
The audit trail leads with who did what. `tw audit`'s one-line help is
"who did what, when and why", and its footer is a quiet
`N records · chain verified` when every chain verifies (per-chain detail
appears only for a failure or a retention-pruned chain; a failure still
prints `chain NOT VERIFIED: …` and exits `audit_unverified`). Scripts that
matched `chain verified: N records` should match `chain verified` or use
`tw audit --json` (`verified`). The admin console shows the audit chain
on the overview and object pages only when it fails verification, and the
audit page's integrity card sits below the records unless a chain fails,
when it moves to the top. `GET /v1/audit/verification` and the
`audit_unverified` error code are unchanged.
