package features

import (
	"slices"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

func TestHostListsStoreKinds(t *testing.T) {
	h := Host()
	for _, want := range []string{"d1", "kv", "r2", Queues, Telemetry} {
		if !slices.Contains(h, want) {
			t.Errorf("Host() = %v, lacks %q", h, want)
		}
	}
	if !slices.IsSorted(h) {
		t.Errorf("Host() not sorted: %v", h)
	}
}

func TestRequired(t *testing.T) {
	files := map[string][]byte{
		"wrangler.jsonc": []byte(`{"name":"app","main":"index.js","compatibility_date":"2026-04-01",
			"kv_namespaces":[{"binding":"CACHE","id":"cache"}],
			"r2_buckets":[{"binding":"B","bucket_name":"b"}]}`),
		"index.js":        []byte(`export default { fetch() { return new Response("ok") } }`),
		"tailworks.jsonc": []byte(`{"tailnet":{"tags":["tag:app"]}}`),
	}
	b, err := bundle.Pack(files)
	if err != nil {
		t.Fatal(err)
	}
	got := RequiredByBundle(b)
	if !slices.Equal(got, []string{"kv", "r2"}) {
		t.Fatalf("RequiredByBundle = %v", got)
	}
	if m := Missing(got, []string{"kv"}); !slices.Equal(m, []string{"r2"}) {
		t.Fatalf("Missing = %v", m)
	}
}

// TestRequiredExitNode: tailnet.egress.via "exit-node" needs a host whose
// internet path is an exit node; the feature is a host setting, so the
// build-static Host() never lists it (hostd adds it).
func TestRequiredExitNode(t *testing.T) {
	for tw, want := range map[string]bool{
		`{"tailnet":{"tags":["tag:app"],"egress":{"via":"exit-node"}}}`:                  true,
		`{"tailnet":{"tags":["tag:app"],"egress":{"via":"exit-node","internet":false}}}`: true,
		`{"tailnet":{"tags":["tag:app"],"egress":{"internet":["api.example.com"]}}}`:     false,
		`{"tailnet":{"tags":["tag:app"]}}`:                                               false,
	} {
		b, err := bundle.Pack(map[string][]byte{
			"wrangler.jsonc":  []byte(`{"name":"app","main":"index.js"}`),
			"index.js":        []byte(`export default {}`),
			"tailworks.jsonc": []byte(tw),
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(RequiredByBundle(b), InternetExitNode); got != want {
			t.Errorf("%s: needs %s = %v, want %v", tw, InternetExitNode, got, want)
		}
	}
	if slices.Contains(Host(), InternetExitNode) {
		t.Errorf("Host() lists %s; it depends on the host's flags", InternetExitNode)
	}
}

// TestRequiredAssets: a bundle with wrangler "assets" needs a host that
// serves them (an older one would load assets/*.js as modules); without
// "assets" an assets/ directory needs nothing.
func TestRequiredAssets(t *testing.T) {
	for w, want := range map[string]bool{
		`{"name":"app","main":"index.js","assets":{"directory":"public"}}`: true,
		`{"name":"app","main":"index.js"}`:                                 false,
		`{"name":"app","assets":{"directory":"dist"}}`:                     true, // assets-only
	} {
		b, err := bundle.Pack(map[string][]byte{
			"wrangler.jsonc":    []byte(w),
			"index.js":          []byte(`export default {}`),
			"assets/index.html": []byte("<h1>hi</h1>"),
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(RequiredByBundle(b), Assets); got != want {
			t.Errorf("%s: requires assets %v, want %v", w, got, want)
		}
	}
	if !slices.Contains(Host(), Assets) {
		t.Errorf("Host() = %v, lacks %q", Host(), Assets)
	}
}

func TestRequiredWorkerInternet(t *testing.T) {
	// The boolean form of tailnet.egress.internet needs a host that parses
	// it; the list form (which older hosts read) and absence need nothing.
	for cfg, want := range map[string]bool{
		`{"tailnet":{"tags":["tag:app"]}}`:                                     false,
		`{"tailnet":{"tags":["tag:app"],"egress":{"internet":["a.example"]}}}`: false,
		`{"tailnet":{"tags":["tag:app"],"egress":{"internet":[]}}}`:            false,
		`{"tailnet":{"tags":["tag:app"],"egress":{"internet":false}}}`:         true,
		`{"tailnet":{"tags":["tag:app"],"egress":{"internet":true}}}`:          true,
	} {
		b, err := bundle.Pack(map[string][]byte{
			"wrangler.jsonc":  []byte(`{"name":"app","main":"index.js","compatibility_date":"2026-04-01"}`),
			"index.js":        []byte(`export default { fetch() { return new Response("ok") } }`),
			"tailworks.jsonc": []byte(cfg),
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := slices.Contains(RequiredByBundle(b), WorkerInternet); got != want {
			t.Errorf("%s: requires worker-internet = %v, want %v", cfg, got, want)
		}
	}
	if !slices.Contains(Host(), WorkerInternet) {
		t.Error("hosts must advertise worker-internet")
	}
}
