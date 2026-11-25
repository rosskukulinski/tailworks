package emailstub

import (
	"os"
	"strings"
	"testing"
)

// TestBuildMJSMatches: create-tailworks' build.mjs stubs cloudflare:email
// with the same text as the Go bundler (it can't import this package).
func TestBuildMJSMatches(t *testing.T) {
	b, err := os.ReadFile("../../js/packages/create-tailworks/lib/build.mjs")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `contents: "`+ClassJS+`",`) {
		t.Fatalf("build.mjs's cloudflare:email stub differs from emailstub.ClassJS:\n%s", ClassJS)
	}
	// The refusal of the app's own import (refuse.go): same message.
	if !strings.Contains(string(b), `"`+Unsupported+` (imported by "`) {
		t.Fatalf("build.mjs's cloudflare:email refusal differs from emailstub.Refusal: %s", Refusal("<file>"))
	}
}

func TestDependency(t *testing.T) {
	for p, want := range map[string]bool{
		"/home/u/app/node_modules/agents/dist/index.js": true,
		"node_modules/x/y.js":                           true,
		"/opt/tw/share/node_modules/@tailworks/a.js":    true,
		"/home/u/app/src/index.ts":                      false,
		"/home/u/app/packages/lib/node_modules_x/a.js":  false,
	} {
		if got := Dependency(p); got != want {
			t.Errorf("Dependency(%q) = %v", p, got)
		}
	}
}
