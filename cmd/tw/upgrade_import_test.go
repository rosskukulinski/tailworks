package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/releasesig"
)

// TestUpgradeImportVerifySums: tw upgrade import <version> trusts the
// release's SHA256SUMS only when SHA256SUMS.sig is a signature of it by a
// pinned key, with no way past that.
func TestUpgradeImportVerifySums(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	signers := filepath.Join(t.TempDir(), "allowed_signers")
	if err := os.WriteFile(signers, []byte(releasesig.AuthorizedKey(pub, "test")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TW_RELEASE_SIGNERS", signers)
	t.Setenv("TW_ALLOW_UNSIGNED", "1") // ignored here
	sums := []byte("0000  tailworks-9.9.9-linux-amd64.tar.gz\n")
	files := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()
	ctx := context.Background()
	for _, c := range []struct {
		name string
		sig  []byte
		code string
	}{
		{"no signature", nil, "release_unsigned"},
		{"signed by another key", releasesig.Sign(other, sums), "release_bad_signature"},
		{"a signature of other sums", releasesig.Sign(priv, []byte("x")), "release_bad_signature"},
		{"signed", releasesig.Sign(priv, sums), ""},
	} {
		delete(files, "/SHA256SUMS.sig")
		if c.sig != nil {
			files["/SHA256SUMS.sig"] = c.sig
		}
		if err := upgradeImportVerifySums(ctx, srv.URL, t.TempDir(), sums); cliCode(err) != c.code {
			t.Errorf("%s: %v, want %q", c.name, err, c.code)
		}
	}
}
