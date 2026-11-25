package main

import "github.com/rosskukulinski/tailworks/internal/termsafe"

// Human-readable output escapes every string a tenant or the internet
// controls (object names, tags, audit detail, deployment and host errors,
// approval params, queue bodies, D1 rows, R2 keys, log lines) so it cannot
// move the cursor, recolour or clear the terminal, or forge a row
// (security/LOOP.md invariant I5, ledger H13). --json output goes through
// termsafe.JSON in emit and the NDJSON writers instead.
var (
	tsafe    = termsafe.String  // one field
	tsafeAll = termsafe.Strings // a list of fields
)
