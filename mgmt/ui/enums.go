package ui

import (
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"github.com/getkin/kin-openapi/openapi3"

	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// The object states (status filter) and audit kinds (event suggestions) are
// the spec's ObjectStatus and AuditKind enums, read from the embedded
// api/openapi.yaml rather than copied (issue #514: the copies drifted).
// Parsed once, on the first /ui/objects or /ui/audit render, not at init:
// cmd/tw links this package and the parse costs milliseconds.

// uiSpecEnums returns the ObjectStatus values in spec (lifecycle) order and
// the AuditKind values sorted (the spec groups them by feature). On a
// parse failure both are empty (an empty select and datalist, logged once),
// never a failed page.
var uiSpecEnums = sync.OnceValues(func() (states, kinds []string) {
	doc, err := v1.Spec()
	if err == nil {
		states, err = uiEnumValues(doc.Components.Schemas, "ObjectStatus")
	}
	if err == nil {
		kinds, err = uiEnumValues(doc.Components.Schemas, "AuditKind")
		slices.Sort(kinds)
	}
	if err != nil {
		slog.Warn("admin UI: no status or event lists", "err", err)
		return nil, nil
	}
	return states, kinds
})

func uiEnumValues(schemas openapi3.Schemas, name string) ([]string, error) {
	ref, ok := schemas[name]
	if !ok || ref.Value == nil || len(ref.Value.Enum) == 0 {
		return nil, fmt.Errorf("spec schema %s has no enum", name)
	}
	out := make([]string, 0, len(ref.Value.Enum))
	for _, v := range ref.Value.Enum {
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("spec schema %s: enum value %v is not a string", name, v)
		}
		out = append(out, s)
	}
	return out, nil
}
