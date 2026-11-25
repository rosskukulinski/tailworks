package pyodide

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/netd/nettest"
)

// TestBundlesMatchWorkerd: every pinned bundle's version and date are
// strings the pinned workerd binary carries (a workerd bump that drops or
// renames one fails here).
func TestBundlesMatchWorkerd(t *testing.T) {
	bin, err := nettest.WorkerdBinary()
	if err != nil {
		t.Skip(err)
	}
	b, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range Bundles {
		parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(p.File, "pyodide_"), ".capnp.bin"), "_")
		if len(parts) != 3 {
			t.Fatalf("%s: want pyodide_<version>_<date>_<n>.capnp.bin", p.File)
		}
		for _, s := range parts[:2] {
			if !bytes.Contains(b, []byte(s)) {
				t.Errorf("%s: the pinned workerd does not know %q; update Bundles", p.File, s)
			}
		}
	}
}

func TestEnsure(t *testing.T) {
	body := []byte("not really pyodide")
	sum := sha256.Sum256(body)
	old := Bundles
	t.Cleanup(func() { Bundles = old })
	Bundles = []Bundle{{File: "pyodide_x.capnp.bin", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(body))}}
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write(body)
	}))
	defer srv.Close()
	dir := filepath.Join(t.TempDir(), "py")
	if err := Ensure(context.Background(), dir, srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(context.Background(), dir, srv.URL+"/"); err != nil || hits != 1 {
		t.Fatalf("second Ensure: %v, %d fetches", err, hits)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "pyodide_x.capnp.bin")); !bytes.Equal(b, body) {
		t.Fatal("bundle not written")
	}

	// A bundle that does not match its pin is refused and not kept.
	Bundles[0].File, Bundles[0].SHA256 = "pyodide_y.capnp.bin", strings.Repeat("0", 64)
	if err := Ensure(context.Background(), dir, srv.URL+"/"); err == nil || !strings.Contains(err.Error(), "does not match the pin") {
		t.Fatalf("bad digest: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pyodide_y.capnp.bin")); err == nil {
		t.Fatal("a bundle with the wrong digest was kept")
	}
}

func TestParseBundles(t *testing.T) {
	if len(Bundles) != 3 || Bundles[1].File != "pyodide_0.28.2_2025-01-16_22.capnp.bin" || Bundles[1].Size != 17501408 || len(Bundles[1].SHA256) != 64 {
		t.Fatalf("bundles.txt: %+v", Bundles)
	}
}
