package bundle

import (
	"strings"
	"testing"
)

// TestInternetEgressPorts: a tailnet.egress.internet entry may name a port
// ("smtp.example.com:25", the port policy's override); a bad port fails
// the parse.
func TestInternetEgressPorts(t *testing.T) {
	var tc twConfig
	if err := UnmarshalJSONC([]byte(`{"tailnet": {"egress": {"internet": ["api.example.com", "smtp.example.com:25", "[2001:db8::1]:25", "2001:db8::1"]}}}`), &tc); err != nil {
		t.Fatal(err)
	}
	if !tc.Tailnet.Egress.Internet.NamesPort() {
		t.Error("NamesPort() = false for a list with smtp.example.com:25")
	}
	if err := UnmarshalJSONC([]byte(`{"tailnet": {"egress": {"internet": ["api.example.com", "2001:db8::25"]}}}`), &tc); err != nil {
		t.Fatal(err)
	}
	if tc.Tailnet.Egress.Internet.NamesPort() {
		t.Error("NamesPort() = true for a list without a port (2001:db8::25 is an address)")
	}
	for _, bad := range []string{`"smtp.example.com:abc"`, `"smtp.example.com:0"`, `"smtp.example.com:70000"`, `"[2001:db8::1]:x"`} {
		err := UnmarshalJSONC([]byte(`{"tailnet": {"egress": {"internet": [`+bad+`]}}}`), &tc)
		if err == nil || !strings.Contains(err.Error(), "tailnet.egress.internet") || !strings.Contains(err.Error(), ":<port>") {
			t.Errorf("%s: err = %v, want a tailnet.egress.internet port error", bad, err)
		}
	}
}
