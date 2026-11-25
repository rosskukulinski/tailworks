package features

import (
	"slices"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// TestRequiredInternetPorts: a tailnet.egress.internet entry that names a
// port (the port policy's override, ADR-052 follow-up) needs a host that
// reads it; an older host would read "smtp.example.com:25" as a name that
// matches nothing.
func TestRequiredInternetPorts(t *testing.T) {
	for cfg, want := range map[string]bool{
		`{"tailnet":{"tags":["tag:app"]}}`:                                                       false,
		`{"tailnet":{"tags":["tag:app"],"egress":{"internet":["a.example"]}}}`:                   false,
		`{"tailnet":{"tags":["tag:app"],"egress":{"internet":["a.example","2001:db8::25"]}}}`:    false,
		`{"tailnet":{"tags":["tag:app"],"egress":{"internet":["a.example","smtp.example:25"]}}}`: true,
		`{"tailnet":{"tags":["tag:app"],"egress":{"internet":["*:25"]}}}`:                        true,
	} {
		b, err := bundle.Pack(map[string][]byte{
			"wrangler.jsonc":  []byte(`{"name":"app","main":"index.js","compatibility_date":"2026-04-01"}`),
			"index.js":        []byte(`export default { fetch() { return new Response("ok") } }`),
			"tailworks.jsonc": []byte(cfg),
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(RequiredByBundle(b), InternetPorts); got != want {
			t.Errorf("%s: requires internet-ports = %v, want %v", cfg, got, want)
		}
	}
	if !slices.Contains(Host(), InternetPorts) {
		t.Error("hosts must advertise internet-ports")
	}
}
