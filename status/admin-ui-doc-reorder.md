# admin-ui-doc-reorder (#513)

docs/ADMIN-UI.md reads top to bottom as the operator doc: URL and who
gets in, Pages, Mutations. The internals follow: "Public API only" moved
from after the admission section to just above "Browser-origin guard",
and "Why plain HTTP on :80" is its own `###` heading inside the guard
section. A short "Guards you will notice" paragraph in the operator part
names the two refusals an operator meets (421 `misdirected_request`,
403 `forbidden` "cross-site request refused") with links to errors.md.

Not done, on purpose: no ADR. The issue's "Depends on" says the reorder
is the minimum; the verifiers split on an ADR. Heading text is
unchanged, so `#browser-origin-guard-csrf-dns-rebinding` (security-model.md)
and `#url-and-who-gets-in` (errors.md) still resolve; no other file
changed. Package comments in mgmt/ui/ui.go and mgmt/uiclient.go and the
Tests section stay.

## Tests

- `sh scripts/docs-only.sh`: yes (1 Markdown file).
- `make lint-docs test-docs`: pass.
- `make changes-check`: ok (no upgrade-relevant paths).
