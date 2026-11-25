package npminstall

import (
	"os"
	"path/filepath"
	"testing"
)

func mkpkg(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if name != "" {
		if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"`+name+`","version":"1.2.3"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tw := filepath.Join(dir, "bin", "tw")
	if err := os.WriteFile(tw, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return tw
}

func TestOf(t *testing.T) {
	root := t.TempDir()
	global := mkpkg(t, filepath.Join(root, "lib", "node_modules", "tailworks", "node_modules", "@tailworks", "cli-linux-arm64"), "@tailworks/cli-linux-arm64")
	npx := mkpkg(t, filepath.Join(root, ".npm", "_npx", "0123abcd", "node_modules", "@tailworks", "cli-darwin-arm64"), "@tailworks/cli-darwin-arm64")
	tarball := mkpkg(t, filepath.Join(root, "tarball"), "")
	other := mkpkg(t, filepath.Join(root, "other"), "esbuild")

	in, ok := Of(global)
	if !ok || in.Package != "@tailworks/cli-linux-arm64" || in.Version != "1.2.3" || in.NPX || in.Kind() != "npm" {
		t.Fatalf("global: %+v %v", in, ok)
	}
	in, ok = Of(npx)
	if !ok || !in.NPX || in.Kind() != "npx (npm's cache)" {
		t.Fatalf("npx: %+v %v", in, ok)
	}
	if _, ok := Of(tarball); ok {
		t.Fatal("a tarball install (no package.json) is not an npm install")
	}
	if _, ok := Of(other); ok {
		t.Fatal("another package's package.json is not a tailworks platform package")
	}
	// A symlink to the binary (npm's .bin links point at the shim, but a
	// user may link bin/tw) resolves to the package.
	link := filepath.Join(root, "tw-link")
	if err := os.Symlink(global, link); err != nil {
		t.Fatal(err)
	}
	if _, ok := Of(link); !ok {
		t.Fatal("symlink to a platform package's tw")
	}
}
