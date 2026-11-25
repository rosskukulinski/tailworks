package hostd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/releasesig"
)

// signRelease adds SHA256SUMS and SHA256SUMS.sig (by priv) to the files of
// a release tarball rooted at top.
func signRelease(t *testing.T, priv ed25519.PrivateKey, top string, files map[string]string) map[string]string {
	t.Helper()
	var names []string
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var sums strings.Builder
	for _, n := range names {
		s := sha256.Sum256([]byte(files[n]))
		sums.WriteString(hex.EncodeToString(s[:]) + "  " + strings.TrimPrefix(n, top+"/") + "\n")
	}
	out := map[string]string{
		top + "/SHA256SUMS":     sums.String(),
		top + "/SHA256SUMS.sig": string(releasesig.Sign(priv, []byte(sums.String()))),
	}
	for n, b := range files {
		out[n] = b
	}
	return out
}

func testReleaseKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// A pushed release is installed only if it is signed by a pinned key: an
// unsigned tarball, one signed by another key (say, by a compromised mgmt),
// or one with a file changed after signing never reaches install.sh.
func TestSelfUpgradeRefusesUnsignedReleases(t *testing.T) {
	t.Parallel()
	pub, priv := testReleaseKey(t)
	_, other := testReleaseKey(t)
	top := "tailworks-0.3.0-linux-amd64"
	files := map[string]string{top + "/install.sh": "#!/bin/sh\necho ran > \"$2/installed\"\n", top + "/VERSION": "0.3.0\n"}
	for name, tgz := range map[string]map[string]string{
		"unsigned":  files,
		"other key": signRelease(t, other, top, files),
		"tampered": func() map[string]string {
			f := signRelease(t, priv, top, files)
			f[top+"/install.sh"] += "evil\n"
			return f
		}(),
		"extra file": func() map[string]string { f := signRelease(t, priv, top, files); f[top+"/bin/extra"] = "x"; return f }(),
	} {
		prefix := t.TempDir()
		h := &Host{cfg: Config{DataDir: t.TempDir(), SelfUpgrade: true, InstallPrefix: prefix, Restart: func() {}}}
		h.upg.signers = []ed25519.PublicKey{pub}
		p := filepath.Join(t.TempDir(), top+".tar.gz")
		os.WriteFile(p, tarGz(t, tgz), 0o600)
		err := h.installArtifact(context.Background(), p, filepath.Join(t.TempDir(), "x"), "0.3.0")
		if err == nil || !strings.Contains(err.Error(), "refusing an unverified release") {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := os.Stat(filepath.Join(prefix, "installed")); err == nil {
			t.Errorf("%s: install.sh ran", name)
		}
	}
	// With no override, the binary's pinned keys are used: a test key isn't one.
	h := &Host{cfg: Config{DataDir: t.TempDir(), SelfUpgrade: true, InstallPrefix: t.TempDir(), Restart: func() {}}}
	p := filepath.Join(t.TempDir(), top+".tar.gz")
	os.WriteFile(p, tarGz(t, signRelease(t, priv, top, files)), 0o600)
	if err := h.installArtifact(context.Background(), p, filepath.Join(t.TempDir(), "x"), "0.3.0"); err == nil {
		t.Fatal("installed a release signed by an unpinned key")
	}
}
