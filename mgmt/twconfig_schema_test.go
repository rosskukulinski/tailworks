package mgmt

import (
	"reflect"
	"testing"

	"github.com/rosskukulinski/tailworks/schema"
)

// Every key mgmt reads from tailworks.jsonc is in the schema, so tw dev and
// tw deploy don't refuse it as unknown.
func TestTwConfigKeysInSchema(t *testing.T) {
	if missing := schema.Unlisted(reflect.TypeFor[TwConfig]()); len(missing) > 0 {
		t.Fatalf("TwConfig fields with no property in schema/tailworks.schema.json: %v", missing)
	}
}
