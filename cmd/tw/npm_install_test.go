package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/npminstall"
)

// mkNPMPackage makes a platform-package-shaped directory: a tarball
// prefix's layout plus npm's package.json.
func mkNPMPackage(t *testing.T, dir string) string {
	t.Helper()
	mkPrefix(t, dir, "0.5.0")
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"@tailworks/cli-linux-arm64","version":"0.5.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func stubNPMSelf(t *testing.T, in npminstall.Install, ok bool) {
	t.Helper()
	old := npmSelf
	npmSelf = func() (npminstall.Install, bool) { return in, ok }
	t.Cleanup(func() { npmSelf = old })
}

// An npm platform package has VERSION and share/tailworks like a tarball
// install, but npm owns it: tw upgrade must not take it for a prefix.
func TestNPMPackageIsNotAnInstallPrefix(t *testing.T) {
	t.Parallel()
	pkg := mkNPMPackage(t, filepath.Join(t.TempDir(), "node_modules", "@tailworks", "cli-linux-arm64"))
	if isInstallPrefix(pkg) {
		t.Fatal("an npm platform package is an install.sh prefix")
	}
	if got := prefixOfBinary(filepath.Join(pkg, "bin", "tw")); got != "" {
		t.Fatalf("prefixOfBinary = %q, want none", got)
	}
	if _, err := resolvePrefix(pkg, "", nil); err == nil {
		t.Fatal("tw upgrade --prefix <npm package> was accepted")
	}
}

func TestNPMUpgradeErr(t *testing.T) {
	t.Parallel()
	var ce *cliErr
	e := npmUpgradeErr(npminstall.Install{Dir: "/g/lib/node_modules/tailworks/node_modules/@tailworks/cli-linux-arm64", Package: "@tailworks/cli-linux-arm64", Version: "0.5.0"})
	if !errors.As(e, &ce) || ce.Code != "npm_install" || !strings.Contains(strings.Join(ce.Fix, "\n"), "npm install -g tailworks@latest") {
		t.Fatalf("global: %+v", e)
	}
	e = npmUpgradeErr(npminstall.Install{Dir: "/h/.npm/_npx/ab/node_modules/@tailworks/cli-linux-arm64", Package: "@tailworks/cli-linux-arm64", NPX: true})
	if !strings.Contains(strings.Join(e.Fix, "\n"), "npx tailworks@latest") {
		t.Fatalf("npx: %+v", e)
	}
}

// A login service must not run tw from npx's cache; a global npm install
// (a stable path) or a tarball install may.
func TestNPMService(t *testing.T) {
	stubNPMSelf(t, npminstall.Install{Dir: "/h/.npm/_npx/ab/node_modules/@tailworks/cli-darwin-arm64", Package: "@tailworks/cli-darwin-arm64", NPX: true}, true)
	var ce *cliErr
	if err := npmServiceErr(); !errors.As(err, &ce) || ce.Code != "npm_install" {
		t.Fatalf("npx: %v", err)
	}
	if _, err := installService(nil); !errors.As(err, &ce) || ce.Code != "npm_install" {
		t.Fatalf("installService from npx: %v", err)
	}
	if got := upgradeCommand("0.6.0"); got != "npx tailworks@latest …" {
		t.Fatalf("upgradeCommand (npx) = %q", got)
	}
	if st, d, _ := doctorInstall(); st != "info" || installFact(d) != "npx" {
		t.Fatalf("doctor (npx): %s %s", st, d)
	}

	stubNPMSelf(t, npminstall.Install{Dir: "/g/lib/node_modules/tailworks/node_modules/@tailworks/cli-darwin-arm64", Package: "@tailworks/cli-darwin-arm64"}, true)
	if err := npmServiceErr(); err != nil {
		t.Fatalf("global npm install: %v", err)
	}
	if got := upgradeCommand("0.6.0"); got != "tw backup && tw down && npm install -g tailworks@0.6.0 && tw up" {
		t.Fatalf("upgradeCommand (npm) = %q", got)
	}
	if st, d, _ := doctorInstall(); st != "ok" || installFact(d) != "npm" || !strings.Contains(d, "microVM") {
		t.Fatalf("doctor (npm): %s %s", st, d)
	}

	stubNPMSelf(t, npminstall.Install{}, false)
	if err := npmServiceErr(); err != nil {
		t.Fatalf("not npm: %v", err)
	}
}
