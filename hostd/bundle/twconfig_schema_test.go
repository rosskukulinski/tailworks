package bundle

import (
	"reflect"
	"testing"

	"github.com/rosskukulinski/tailworks/schema"
)

// Every key hostd reads from tailworks.jsonc is in the schema, so tw dev and
// tw deploy don't refuse it as unknown (tailnet.egress.via was, #440).
func TestTwConfigKeysInSchema(t *testing.T) {
	if missing := schema.Unlisted(reflect.TypeFor[twConfig]()); len(missing) > 0 {
		t.Fatalf("twConfig fields with no property in schema/tailworks.schema.json: %v", missing)
	}
}
