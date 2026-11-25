package v1

import (
	"math/rand"
	"regexp"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

// TestValidBase64CharsMatchesRegexp: the fast `format: byte` check accepts
// exactly what kin-openapi's regexp does.
func TestValidBase64CharsMatchesRegexp(t *testing.T) {
	re := regexp.MustCompile(openapi3.FormatOfStringByte)
	check := func(s string) {
		t.Helper()
		if got, want := validBase64Chars(s) == nil, re.MatchString(s); got != want {
			t.Fatalf("%q: fast=%v regexp=%v", s, got, want)
		}
	}
	for _, s := range []string{"", "=", "==", "U3dhZ2dlciByb2Nrcw==", "a-_+/9Z", "ab=c", "ab==\n", "\nab", "a b", "é", "ab=", "=a", "a\x00"} {
		check(s)
	}
	alphabet := "aZ09+/-_=\n .é\x00"
	rng := rand.New(rand.NewSource(1))
	for range 20000 {
		b := make([]byte, rng.Intn(8))
		for i := range b {
			b[i] = alphabet[rng.Intn(len(alphabet))]
		}
		check(string(b))
	}
}

// TestValidatorByteFormat: the Validator's schema options carry the fast
// check, and a value outside the alphabet is still refused.
func TestValidatorByteFormat(t *testing.T) {
	s := &openapi3.Schema{Type: &openapi3.Types{"string"}, Format: "byte"}
	if err := s.VisitJSON("U3dhZ2dlciByb2Nrcw==", schemaOpts...); err != nil {
		t.Fatal(err)
	}
	if err := s.VisitJSON("not base64!", schemaOpts...); err == nil {
		t.Fatal("a non-base64 string passed format: byte")
	}
}
