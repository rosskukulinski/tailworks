package container

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestParseBuild: wrangler's image_build_context/image_vars (the default
// image) and images.<name>.build_context/build_vars land on Class.Build,
// which never leaves the developer's machine (no JSON); tailworks.jsonc
// can't set them.
func TestParseBuild(t *testing.T) {
	t.Parallel()
	w := []byte(`{"containers": [{"class_name": "Sbx", "image": "./docker/Dockerfile",
  "image_build_context": ".", "image_vars": {"B": "2", "A": "1"},
  "images": {"py": {"dockerfile": "./py/Dockerfile", "build_context": "./py/ctx", "build_vars": {"V": "3"}}, "plain": {"dockerfile": "./p/Dockerfile"}}}]}`)
	cs, err := Parse(w, nil)
	if err != nil {
		t.Fatal(err)
	}
	b := cs[0].Build
	want := &Build{Context: ".", Vars: map[string]string{"A": "1", "B": "2"},
		ImageContexts: map[string]string{"py": "./py/ctx"}, ImageVars: map[string]map[string]string{"py": {"V": "3"}}}
	if !reflect.DeepEqual(b, want) {
		t.Fatalf("build %+v", b)
	}
	if b.ContextFor("") != "." || b.ContextFor("py") != "./py/ctx" || b.ContextFor("plain") != "" || b.VarsFor("py")["V"] != "3" || b.VarsFor("plain") != nil {
		t.Fatalf("lookups on %+v", b)
	}
	if j, _ := json.Marshal(cs[0]); strings.Contains(string(j), "Vars") || strings.Contains(string(j), "Context") {
		t.Fatalf("build vars serialized: %s", j)
	}
	var nb *Build
	if nb.ContextFor("") != "" || nb.VarsFor("x") != nil {
		t.Fatal("nil Build")
	}
	if _, err := Parse(w, []byte(`{"containers": {"Sbx": {"image_vars": {"A": "1"}}}}`)); err == nil || !strings.Contains(err.Error(), "wrangler.jsonc") {
		t.Fatalf("tailworks.jsonc image_vars: %v", err)
	}
}
