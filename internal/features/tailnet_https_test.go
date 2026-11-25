package features

import (
	"slices"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// TestRequiredTailnetHTTPS: tailnet.https needs a host that opens :443 on
// the workload node; an older host would ignore the key and serve http://
// only, so the app's https:// URL would never answer.
func TestRequiredTailnetHTTPS(t *testing.T) {
	for cfg, want := range map[string]bool{
		`{"tailnet":{"tags":["tag:app"]}}`:               false,
		`{"tailnet":{"tags":["tag:app"],"https":false}}`: false,
		`{"tailnet":{"tags":["tag:app"],"https":true}}`:  true,
	} {
		b, err := bundle.Pack(map[string][]byte{
			"wrangler.jsonc":  []byte(`{"name":"app","main":"index.js","compatibility_date":"2026-04-01"}`),
			"index.js":        []byte(`export default { fetch() { return new Response("ok") } }`),
			"tailworks.jsonc": []byte(cfg),
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(RequiredByBundle(b), TailnetHTTPS); got != want {
			t.Errorf("%s: requires tailnet-https = %v, want %v", cfg, got, want)
		}
	}
	// The Node tier reads it too.
	b, err := bundle.Pack(map[string][]byte{
		"tailworks.jsonc": []byte(`{"name":"app","runtime":"node","main":"server.js","tailnet":{"tags":["tag:app"],"https":true}}`),
		"server.js":       []byte(`require("http").createServer((q, s) => s.end("ok")).listen(process.env.PORT)`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(RequiredByBundle(b), TailnetHTTPS) {
		t.Error("runtime node with tailnet.https: tailnet-https not required")
	}
	if !slices.Contains(Host(), TailnetHTTPS) {
		t.Error("hosts must advertise tailnet-https")
	}
}
