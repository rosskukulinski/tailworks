package hostd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func tarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		mode := int64(0o644)
		if strings.HasSuffix(name, ".sh") {
			mode = 0o755
		}
		tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

func TestExtractTarGzRefusesEscapes(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for name, files := range map[string]map[string]string{
		"dotdot":   {"../evil": "x"},
		"absolute": {"/tmp/evil": "x"},
	} {
		p := filepath.Join(dir, name+".tgz")
		os.WriteFile(p, tarGz(t, files), 0o600)
		if err := extractTarGz(p, filepath.Join(dir, name)); err == nil {
			t.Errorf("%s: extracted", name)
		}
	}
	p := filepath.Join(dir, "ok.tgz")
	os.WriteFile(p, tarGz(t, map[string]string{"tailworks-0.2.0-linux-amd64/install.sh": "#!/bin/sh\n"}), 0o600)
	if err := extractTarGz(p, filepath.Join(dir, "ok")); err != nil {
		t.Fatal(err)
	}
}

// installArtifact runs the tarball's install.sh into the prefix.
func TestSelfUpgradeInstalls(t *testing.T) {
	t.Parallel()
	prefix := t.TempDir()
	pub, priv := testReleaseKey(t)
	tgz := tarGz(t, signRelease(t, priv, "tailworks-0.2.0-linux-amd64", map[string]string{
		"tailworks-0.2.0-linux-amd64/install.sh": "#!/bin/sh\n[ \"$1\" = --prefix ] || exit 3\necho 0.2.0 > \"$2/installed\"\n",
		"tailworks-0.2.0-linux-amd64/VERSION":    "0.2.0\n",
	}))
	h := &Host{cfg: Config{DataDir: t.TempDir(), SelfUpgrade: true, InstallPrefix: prefix, Restart: func() {}}}
	h.upg.signers = []ed25519.PublicKey{pub}
	p := filepath.Join(t.TempDir(), "tailworks-0.2.0-linux-amd64.tar.gz")
	os.WriteFile(p, tgz, 0o600)
	if err := h.installArtifact(context.Background(), p, filepath.Join(t.TempDir(), "x"), "0.2.0"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(prefix, "installed")); strings.TrimSpace(string(b)) != "0.2.0" {
		t.Fatalf("install.sh did not run into the prefix: %q", b)
	}
	// A failing install.sh is an error (the old release stays).
	bad := tarGz(t, signRelease(t, priv, "tailworks-0.2.0-linux-amd64", map[string]string{"tailworks-0.2.0-linux-amd64/install.sh": "#!/bin/sh\necho nope; exit 1\n", "tailworks-0.2.0-linux-amd64/VERSION": "0.2.0\n"}))
	os.WriteFile(p, bad, 0o600)
	if err := h.installArtifact(context.Background(), p, filepath.Join(t.TempDir(), "y"), "0.2.0"); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("failing install.sh: %v", err)
	}
	if !h.selfUpgradeEnabled() || !slices.Contains(h.features(), FeatureSelfUpgrade) {
		t.Fatalf("self-upgrade not offered: %v", h.features())
	}
	if slices.Contains((&Host{}).features(), FeatureSelfUpgrade) {
		t.Fatal("self-upgrade offered without -self-upgrade")
	}
}

// approvals.json: records from before versioning load; a newer version is
// not loaded; saves stamp "v":1.
func TestApprovalsFormat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	p := filepath.Join(dir, "approvals.json")
	os.WriteFile(p, []byte(`[{"tenantID":"t","workload":"w","workflow":"f","instance":"i","step":"s","type":"x","status":"pending","requestedAt":"2026-11-15T00:00:00Z"}]`), 0o600)
	h := &Host{cfg: Config{DataDir: dir}, log: slog.New(slog.DiscardHandler)}
	s := h.approvals()
	if len(s.m) != 1 {
		t.Fatalf("v0 record not loaded: %v", s.m)
	}
	s.mu.Lock()
	s.saveLocked()
	s.mu.Unlock()
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), `"v": 1`) {
		t.Fatalf("save without v: %s", b)
	}
	os.WriteFile(p, []byte(`[{"v":2,"tenantID":"t","workload":"w","workflow":"f","instance":"i","step":"s"}]`), 0o600)
	h2 := &Host{cfg: Config{DataDir: dir}, log: slog.New(slog.DiscardHandler)}
	if n := len(h2.approvals().m); n != 0 {
		t.Fatalf("a newer record loaded (%d)", n)
	}
}

// tarEntries builds a .tar.gz from entries in order (name, linkname or body).
type tarEntry struct {
	name, link, body string
	typ              byte
}

func tarGzEntries(t *testing.T, es []tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range es {
		hd := &tar.Header{Name: e.name, Linkname: e.link, Typeflag: e.typ, Mode: 0o644, Size: int64(len(e.body))}
		if e.typ != tar.TypeReg {
			hd.Size = 0
		}
		if err := tw.WriteHeader(hd); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(e.body))
	}
	tw.Close()
	zw.Close()
	return buf.Bytes()
}

// TestExtractTarGzSymlinkEscapes: extraction runs before the signature
// check, so hostile tarballs must not write, or leave a link, outside dir
// (the PR #79 review's chain: a -> ., a/b -> .., then b/evil).
func TestExtractTarGzSymlinkEscapes(t *testing.T) {
	t.Parallel()
	sym := func(name, link string) tarEntry { return tarEntry{name: name, link: link, typ: tar.TypeSymlink} }
	reg := func(name string) tarEntry { return tarEntry{name: name, body: "x", typ: tar.TypeReg} }
	for name, es := range map[string][]tarEntry{
		"write through a chain": {sym("a", "."), sym("a/b", ".."), reg("b/evil")},
		"link through a link":   {sym("s", "."), sym("x", "s/..")},
		"write through a link":  {sym("d", "."), sym("d/up", "../.."), reg("up/evil")},
		"absolute link":         {sym("abs", "/etc")},
		"replace a file":        {reg("f"), reg("f")},
		"replace a link":        {sym("l", "f"), reg("l")},
	} {
		base := t.TempDir()
		dir := filepath.Join(base, "x")
		p := filepath.Join(base, "t.tgz")
		os.WriteFile(p, tarGzEntries(t, es), 0o600)
		if err := extractTarGz(p, dir); err == nil {
			t.Errorf("%s: extracted", name)
		}
		for _, f := range []string{"evil", "../evil"} {
			if _, err := os.Lstat(filepath.Join(base, f)); err == nil {
				t.Errorf("%s: wrote %s outside the directory", name, f)
			}
		}
	}
	// In-tree links (node_modules/.bin style) and dangling in-tree links are fine.
	base := t.TempDir()
	p := filepath.Join(base, "ok.tgz")
	os.WriteFile(p, tarGzEntries(t, []tarEntry{
		{name: "r/", typ: tar.TypeDir},
		reg("r/lib/cli.js"),
		sym("r/bin/cli", "../lib/cli.js"),
		sym("r/bin/gone", "../lib/missing.js"),
	}), 0o600)
	if err := extractTarGz(p, filepath.Join(base, "x")); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(base, "x", "r", "bin", "cli")); err != nil || string(b) != "x" {
		t.Fatalf("in-tree link: %q %v", b, err)
	}
}

// TestSelfUpgradeRefusesOtherVersion: a genuine signed release of another
// version (an older one with a known bug, say) under the ordered version's
// name is refused.
func TestSelfUpgradeRefusesOtherVersion(t *testing.T) {
	t.Parallel()
	prefix := t.TempDir()
	pub, priv := testReleaseKey(t)
	top := "tailworks-0.3.0-linux-amd64"
	tgz := tarGz(t, signRelease(t, priv, top, map[string]string{
		top + "/install.sh": "#!/bin/sh\necho ran > \"$2/installed\"\n",
		top + "/VERSION":    "0.2.0\n",
	}))
	h := &Host{cfg: Config{DataDir: t.TempDir(), SelfUpgrade: true, InstallPrefix: prefix, Restart: func() {}}}
	h.upg.signers = []ed25519.PublicKey{pub}
	p := filepath.Join(t.TempDir(), top+".tar.gz")
	os.WriteFile(p, tgz, 0o600)
	if err := h.installArtifact(context.Background(), p, filepath.Join(t.TempDir(), "x"), "0.3.0"); err == nil || !strings.Contains(err.Error(), "the order was for 0.3.0") {
		t.Fatalf("other version: %v", err)
	}
	if _, err := os.Stat(filepath.Join(prefix, "installed")); err == nil {
		t.Fatal("install.sh ran")
	}
}
