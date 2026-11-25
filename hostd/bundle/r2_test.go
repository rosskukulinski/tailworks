package bundle_test

import (
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// TestParseR2Buckets: r2_buckets entries are stores of kind "r2" named by
// bucket_name (the binding when absent); two bindings of one bucket are
// refused.
func TestParseR2Buckets(t *testing.T) {
	parse := func(buckets string) (*bundle.App, error) {
		return bundle.Parse(map[string][]byte{
			"index.js":       []byte("export default {}"),
			"wrangler.jsonc": []byte(`{ "name": "w", "main": "index.js", "r2_buckets": ` + buckets + ` }`),
		})
	}
	a, err := parse(`[{ "binding": "PHOTOS", "bucket_name": "photos", "preview_bucket_name": "photos-dev", "jurisdiction": "eu" }, { "binding": "LOGS" }]`)
	if err != nil {
		t.Fatal(err)
	}
	want := []bundle.StoreBinding{{Kind: "r2", Binding: "PHOTOS", Name: "photos"}, {Kind: "r2", Binding: "LOGS", Name: "LOGS"}}
	if len(a.Stores) != 2 || a.Stores[0] != want[0] || a.Stores[1] != want[1] {
		t.Fatalf("stores: %+v", a.Stores)
	}
	for buckets, wantErr := range map[string]string{
		`[{ "binding": "A", "bucket_name": "x" }, { "binding": "B", "bucket_name": "x" }]`: "same bucket",
		`[{ "binding": "A", "bucket_name": "a/b" }]`:                                       "store name",
		`{ "binding": "A" }`: "r2_buckets",
	} {
		if _, err := parse(buckets); err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("%s: %v, want %q", buckets, err, wantErr)
		}
	}
}
