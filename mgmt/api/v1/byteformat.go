package v1

import (
	"fmt"

	"github.com/getkin/kin-openapi/openapi3"
)

// schemaOpts are the schema options for every request and response the
// Validator checks.
//
// kin-openapi checks `format: byte` with a regexp
// (openapi3.FormatOfStringByte), which Go runs on its slow NFA matcher. A
// multi-MiB value (a store journal append at mgmt's 4 MiB limit, base64
// in JSON) took seconds to validate, and tens of seconds under -race on a
// loaded CI runner: long enough for the workload lease of
// TestStoresLimits (45 s, never heartbeated) to expire mid-test.
// validBase64Chars accepts exactly the same strings in one pass.
var schemaOpts = []openapi3.SchemaValidationOption{
	openapi3.WithStringFormatValidator("byte", openapi3.NewCallbackValidator(validBase64Chars)),
}

// validBase64Chars is openapi3.FormatOfStringByte, `^[a-zA-Z0-9+/\-_]*=*$`
// (the standard or URL-safe alphabet, then any padding), without the
// regexp.
func validBase64Chars(s string) error {
	i := 0
	for ; i < len(s); i++ {
		c := s[i]
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '+' || c == '/' || c == '-' || c == '_') {
			break
		}
	}
	for ; i < len(s); i++ {
		if s[i] != '=' {
			return fmt.Errorf(`string doesn't match pattern "%s"`, openapi3.FormatOfStringByte)
		}
	}
	return nil
}
