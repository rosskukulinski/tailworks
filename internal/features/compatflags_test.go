package features

import (
	"slices"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// TestRequiredExperimentalFlags (#337): a bundle whose compatibility_flags
// list an experimental flag needs a host that allows them; the feature is
// a host setting, so the build-static Host() never lists it (hostd adds
// it with -allow-experimental-flags).
func TestRequiredExperimentalFlags(t *testing.T) {
	mk := func(flags string) []byte {
		b, err := bundle.Pack(map[string][]byte{
			"wrangler.jsonc":  []byte(`{"name":"app","main":"index.js","compatibility_date":"2026-04-01","compatibility_flags":[` + flags + `]}`),
			"index.js":        []byte(`export default { fetch() { return new Response("ok") } }`),
			"tailworks.jsonc": []byte(`{"tailnet":{"tags":["tag:app"]}}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	if got := RequiredByBundle(mk(`"nodejs_compat","unsafe_module"`)); !slices.Equal(got, []string{ExperimentalFlags}) {
		t.Fatalf("RequiredByBundle(unsafe_module) = %v", got)
	}
	if got := RequiredByBundle(mk(`"nodejs_compat"`)); slices.Contains(got, ExperimentalFlags) {
		t.Fatalf("RequiredByBundle(nodejs_compat) = %v", got)
	}
	if slices.Contains(Host(), ExperimentalFlags) {
		t.Fatalf("Host() = %v lists %s, a host setting", Host(), ExperimentalFlags)
	}
}
