package hostd

import "github.com/rosskukulinski/tailworks/internal/termsafe"

// badObjectName refuses a new object name with a C0, DEL, C1 or bidi
// control (#185, ledger F9 input half). Tenant code chooses object names;
// they end up in audit rows, MagicDNS-adjacent displays and other tenants'
// tooling. It is checked only where a name would be created (ensure,
// admit), never on an object that already exists: existing names are not
// backfilled and still render through termsafe.
func badObjectName(class, name string) *apiError {
	if err := termsafe.CheckName(name); err != nil {
		return &apiError{Code: "bad_object_name", Detail: "object name " + class + "/" + termsafe.String(name) + ": " + err.Error()}
	}
	return nil
}
