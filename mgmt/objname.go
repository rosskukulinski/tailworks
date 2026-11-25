package mgmt

import "github.com/rosskukulinski/tailworks/internal/termsafe"

// checkObjectName refuses a new object name with a C0, DEL, C1 or bidi
// control (#185, ledger F9 input half): tenant code chooses object names,
// and they end up in audit rows, `tw ls`, the admin UI and other tenants'
// tooling. hostd checks the same at ensure/admit; this is the backstop for
// every other caller. It runs after the by-name lookup, so an object that
// already has such a name is still answered idempotently: existing names
// are not backfilled (they render through termsafe).
func checkObjectName(name string) *Error {
	if err := termsafe.CheckName(name); err != nil {
		return errf(400, "bad_object_name", "objectName %s: %v", termsafe.String(name), err)
	}
	return nil
}
