package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/releasesig"
	twversion "github.com/rosskukulinski/tailworks/internal/version"
)

// TestUpgradePlanGolden pins `tw upgrade --plan` on a tw up machine (text
// and --json) for a signed release whose catalog has a breaking change, an
// operator action, a contract migration and a new workerd (issue #111).
// The versions are past every embedded catalog entry, so only the
// release's own catalog shows. Run with -update to rewrite the golden file.
func TestUpgradePlanGolden(t *testing.T) {
	if _, err := os.Stat("/etc/tailworks/setup.json"); err == nil {
		t.Skip("this machine has the microVM helper installed: the plan warns about it")
	}
	work := t.TempDir()
	prefix := filepath.Join(work, "prefix")
	for _, d := range []string{filepath.Join(prefix, "share", "tailworks"), filepath.Join(prefix, "bin")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(prefix, "VERSION"), []byte("9.0.0\n"), 0o644)

	rel := filepath.Join(work, "tailworks-9.1.0")
	os.MkdirAll(filepath.Join(rel, "share", "tailworks"), 0o755)
	os.WriteFile(filepath.Join(rel, "share", "tailworks", "release.json"),
		[]byte(fmt.Sprintf(`{"version":"9.1.0","os":%q,"arch":%q,"workerd":"1.29990101.0"}`, runtime.GOOS, runtime.GOARCH)), 0o644)
	os.WriteFile(filepath.Join(rel, "share", "tailworks", "catalog.json"), []byte(`[{"version": "9.1.0", "date": "2099-01-01", "workerd": "1.29990101.0",
  "contractMigrations": ["0099_drop_legacy"],
  "notes": [
    {"kind": "breaking", "source": "drop-legacy.md", "text": "tw-mgmt no longer reads the legacy table.\nMore detail."},
    {"kind": "feature", "source": "new-flag.md", "text": "A new flag. Operator action: set it."},
    {"kind": "fix", "upgrade": ["schema-contract"], "source": "contract.md", "text": "The store drops a column."},
    {"kind": "fix", "source": "quiet.md", "text": "Not shown in the plan."}
  ]}]`), 0o644)
	os.WriteFile(filepath.Join(rel, "install.sh"), []byte("#!/bin/sh\nexit 1\n"), 0o755)
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	var sums strings.Builder
	for _, f := range []string{"install.sh", "share/tailworks/catalog.json", "share/tailworks/release.json"} {
		s, err := sha256File(filepath.Join(rel, f))
		if err != nil {
			t.Fatal(err)
		}
		sums.WriteString(s + "  " + f + "\n")
	}
	os.WriteFile(filepath.Join(rel, releasesig.SumsFile), []byte(sums.String()), 0o644)
	os.WriteFile(filepath.Join(rel, releasesig.SigFile), releasesig.Sign(key, []byte(sums.String())), 0o644)
	signers := filepath.Join(work, "allowed_signers")
	os.WriteFile(signers, []byte(releasesig.AuthorizedKey(pub, "test")+"\n"), 0o644)
	t.Setenv("TW_RELEASE_SIGNERS", signers)
	t.Setenv("TW_ALLOW_UNSIGNED", "")

	state := filepath.Join(work, "state")
	args := []string{"--state-dir", state, "--prefix", prefix, "--from", rel, "--plan"}
	text, err := captureStdout(t, func() error { return cmdUpgrade(args) })
	if err != nil {
		t.Fatalf("tw upgrade --plan: %v", err)
	}
	js, err := captureStdout(t, func() error { return cmdUpgrade(append(slices.Clone(args), "--json")) })
	jsonOut = false
	if err != nil {
		t.Fatalf("tw upgrade --plan --json: %v", err)
	}
	got := strings.NewReplacer(work, "<work>", twversion.WorkerdPin, "<workerd pin>").Replace("$ tw upgrade --plan\n" + text + "\n$ tw upgrade --plan --json\n" + js)

	golden := filepath.Join("testdata", "upgrade-plan.golden")
	// -update is defined by agent_setup_test.go (package main_test, same test binary).
	if f := flag.Lookup("update"); f != nil && f.Value.String() == "true" {
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Fatalf("tw upgrade --plan output changed (run go test ./cmd/tw -run TestUpgradePlanGolden -update, and review the diff):\n%s", got)
	}
}

func sha256File(p string) (string, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(b)), nil
}
