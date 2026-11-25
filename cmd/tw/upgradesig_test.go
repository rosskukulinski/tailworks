package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/releasesig"
)

// fakeRelease is the files of a tailworks-<v>-<os>-<arch> tarball for
// this machine whose install.sh, if it ever ran, would leave a marker file.
func fakeRelease(v, marker string) (string, map[string]string) {
	top := fmt.Sprintf("tailworks-%s-%s-%s", v, runtime.GOOS, runtime.GOARCH)
	return top, map[string]string{
		top + "/install.sh":                     "#!/bin/sh\necho ran > '" + marker + "'\n",
		top + "/VERSION":                        v + "\n",
		top + "/bin/tw":                         "#!/bin/sh\n",
		top + "/share/tailworks/release.json":   fmt.Sprintf(`{"version":%q,"os":%q,"arch":%q}`, v, runtime.GOOS, runtime.GOARCH),
		top + "/share/tailworks/catalog.json":   `{"releases":[]}`,
		top + "/share/tailworks/placeholder.md": "x\n",
	}
}

// signFiles adds SHA256SUMS and SHA256SUMS.sig (by priv) at top.
func signFiles(priv ed25519.PrivateKey, top string, files map[string]string) map[string]string {
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
		top + "/" + releasesig.SumsFile: sums.String(),
		top + "/" + releasesig.SigFile:  string(releasesig.Sign(priv, []byte(sums.String()))),
	}
	for n, b := range files {
		out[n] = b
	}
	return out
}

// writeRelease writes files as <dir>/<top>.tar.gz with an (attacker-made,
// for the unsigned cases) SHA256SUMS next to it, and returns the tarball.
func writeRelease(t *testing.T, dir, top string, files map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for name, body := range files {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.Close()
	zw.Close()
	p := filepath.Join(dir, top+".tar.gz")
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(buf.Bytes())
	os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(hex.EncodeToString(s[:])+"  "+top+".tar.gz\n"), 0o644)
	return p
}

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

// trustKey points TW_RELEASE_SIGNERS at an allowed_signers file with pub.
func trustKey(t *testing.T, pub ed25519.PublicKey) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "allowed_signers")
	os.WriteFile(p, []byte(releasesig.AuthorizedKey(pub, "test")+"\n"), 0o644)
	t.Setenv("TW_RELEASE_SIGNERS", p)
}

func errCode(err error) string {
	var ce *cliErr
	if errors.As(err, &ce) {
		return ce.Code
	}
	return ""
}

// Issue #98: `tw upgrade` on a tw up machine refuses a release that isn't
// signed by a trusted key, on --plan and on a real run, from a tarball or
// a directory, and never runs anything in it.
func TestUpgradeRefusesUnverifiedReleases(t *testing.T) {
	t.Setenv("TW_ALLOW_UNSIGNED", "")
	pub, priv := testKey(t)
	_, other := testKey(t)
	trustKey(t, pub)
	marker := filepath.Join(t.TempDir(), "install-ran")
	top, files := fakeRelease("9.9.9", marker)
	cases := map[string]struct {
		files map[string]string
		code  string
	}{
		"unsigned":  {files, "release_unsigned"},
		"other key": {signFiles(other, top, files), "release_bad_signature"},
		"tampered": {func() map[string]string {
			f := signFiles(priv, top, files)
			f[top+"/install.sh"] += "evil\n"
			return f
		}(), "release_bad_signature"},
		"extra file": {func() map[string]string {
			f := signFiles(priv, top, files)
			f[top+"/bin/extra"] = "x"
			return f
		}(), "release_bad_signature"},
	}
	for name, tc := range cases {
		tgz := writeRelease(t, t.TempDir(), top, tc.files)
		stage := t.TempDir()
		dir, err := extractRelease(tgz, stage)
		if err != nil {
			t.Fatalf("%s: extract: %v", name, err)
		}
		for _, run := range []struct {
			what string
			args []string
		}{
			{"--plan tarball", []string{"--plan", "--from", tgz}},
			{"--plan dir", []string{"--plan", "--from", dir}},
			{"run tarball", []string{"--yes", "--from", tgz, "--prefix", mkPrefix(t, t.TempDir(), "0.2.0")}},
			{"run dir", []string{"--yes", "--from", dir, "--prefix", mkPrefix(t, t.TempDir(), "0.2.0")}},
		} {
			state := t.TempDir()
			err := cmdUpgrade(append(run.args, "--state-dir", state, "--json"))
			if got := errCode(err); got != tc.code {
				t.Errorf("%s, %s: err = %v (code %q), want %s", name, run.what, err, got, tc.code)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatalf("%s, %s: the release's install.sh ran", name, run.what)
			}
			if _, err := os.Stat(filepath.Join(state, "upgrade")); err == nil {
				t.Errorf("%s, %s: the staging dir was left behind", name, run.what)
			}
			if _, err := os.Stat(filepath.Join(state, "backups")); err == nil {
				t.Errorf("%s, %s: a backup was taken before the signature check", name, run.what)
			}
		}
	}
}

// A release signed by a trusted key gets a plan; --allow-unsigned (or
// TW_ALLOW_UNSIGNED=1) lets an unsigned one through, with a warning.
func TestUpgradeAcceptsSignedReleases(t *testing.T) {
	t.Setenv("TW_ALLOW_UNSIGNED", "")
	pub, priv := testKey(t)
	trustKey(t, pub)
	marker := filepath.Join(t.TempDir(), "install-ran")
	top, files := fakeRelease("9.9.9", marker)

	signed := writeRelease(t, t.TempDir(), top, signFiles(priv, top, files))
	if err := cmdUpgrade([]string{"--plan", "--from", signed, "--state-dir", t.TempDir(), "--json"}); err != nil {
		t.Fatalf("signed release: %v", err)
	}

	unsigned := writeRelease(t, t.TempDir(), top, files)
	if err := cmdUpgrade([]string{"--plan", "--from", unsigned, "--allow-unsigned", "--state-dir", t.TempDir(), "--json"}); err != nil {
		t.Fatalf("--allow-unsigned: %v", err)
	}
	w, err := verifyUpgradeRelease(mustExtract(t, unsigned), true)
	if err != nil || !strings.Contains(w, "not checked") {
		t.Fatalf("--allow-unsigned warning = %q, %v", w, err)
	}
	t.Setenv("TW_ALLOW_UNSIGNED", "1")
	if err := cmdUpgrade([]string{"--plan", "--from", unsigned, "--state-dir", t.TempDir(), "--json"}); err != nil {
		t.Fatalf("TW_ALLOW_UNSIGNED=1: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("--plan ran install.sh")
	}
}

// With no override, the keys pinned in the binary are used: a test key
// isn't one of them.
func TestUpgradeUsesPinnedKeys(t *testing.T) {
	t.Setenv("TW_ALLOW_UNSIGNED", "")
	t.Setenv("TW_RELEASE_SIGNERS", "")
	_, priv := testKey(t)
	top, files := fakeRelease("9.9.9", filepath.Join(t.TempDir(), "m"))
	tgz := writeRelease(t, t.TempDir(), top, signFiles(priv, top, files))
	err := cmdUpgrade([]string{"--plan", "--from", tgz, "--state-dir", t.TempDir(), "--json"})
	if errCode(err) != "release_bad_signature" {
		t.Fatalf("err = %v, want release_bad_signature", err)
	}
}

// tw upgrade extracts with the hardened extractor tw-hostd uses.
func TestExtractReleaseRefusesEscapes(t *testing.T) {
	t.Parallel()
	for name, entry := range map[string]*tar.Header{
		"dotdot":   {Name: "../evil", Typeflag: tar.TypeReg},
		"absolute": {Name: "/tmp/evil", Typeflag: tar.TypeReg},
		"symlink":  {Name: "tailworks-1/x", Typeflag: tar.TypeSymlink, Linkname: "../../../etc"},
	} {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		tw := tar.NewWriter(zw)
		tw.WriteHeader(entry)
		tw.Close()
		zw.Close()
		p := filepath.Join(t.TempDir(), "r.tar.gz")
		os.WriteFile(p, buf.Bytes(), 0o644)
		if _, err := extractRelease(p, t.TempDir()); err == nil {
			t.Errorf("%s: extracted", name)
		}
	}
}

func mustExtract(t *testing.T, tgz string) string {
	t.Helper()
	d, err := extractRelease(tgz, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}
