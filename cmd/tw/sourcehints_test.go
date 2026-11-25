package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSourceCheckoutHints: run from a tailworks checkout (the tests' working
// directory is cmd/tw in one), the fix for missing JS packages is
// `make deps` there; a build_failed for an unresolved @tailworks/* import gets
// it, other build errors don't.
func TestSourceCheckoutHints(t *testing.T) {
	t.Parallel()
	root := sourceCheckout()
	wd, _ := os.Getwd()
	if want, _ := filepath.Abs(filepath.Join(wd, "..", "..")); root != want {
		t.Fatalf("sourceCheckout() = %q, want %q", root, want)
	}
	if fx := reinstallFix("reinstall tw"); !strings.Contains(fx, "make deps") || !strings.Contains(fx, root) {
		t.Fatalf("reinstallFix: %q", fx)
	}
	out := `src/index.ts:1:22: ERROR: Could not resolve "@tailworks/tailnet"`
	e := withBundleFix(fail("build_failed", "esbuild failed for /app", out), out)
	if len(e.Fix) != 1 || !strings.Contains(e.Fix[0], "make deps") {
		t.Fatalf("unresolved @tailworks/tailnet: fix %q", e.Fix)
	}
	other := `src/index.ts:3:1: ERROR: Expected ";" but found "}"`
	if e := withBundleFix(fail("build_failed", "esbuild failed for /app", other), other); len(e.Fix) != 0 {
		t.Fatalf("syntax error got a fix: %q", e.Fix)
	}
	if s := noNodeModules(nil, "/x/create-tailworks"); !strings.Contains(s, "no node_modules") {
		t.Fatalf("noNodeModules: %q", s)
	}
}
