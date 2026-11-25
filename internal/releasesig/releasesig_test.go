package releasesig

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func TestSignVerify(t *testing.T) {
	pub, priv := newKey(t)
	other, _ := newKey(t)
	msg := []byte("abc  tailworks-0.3.0-linux-amd64.tar.gz\n")
	sig := Sign(priv, msg)
	if err := Verify([]ed25519.PublicKey{other, pub}, msg, sig); err != nil {
		t.Fatal(err)
	}
	if err := Verify([]ed25519.PublicKey{pub}, append(msg, 'x'), sig); err == nil {
		t.Fatal("verified a changed message")
	}
	if err := Verify([]ed25519.PublicKey{other}, msg, sig); err == nil || !strings.Contains(err.Error(), "not a pinned release key") {
		t.Fatalf("an unpinned key: %v", err)
	}
	if err := Verify(nil, msg, sig); !errors.Is(err, ErrNoKeys) {
		t.Fatalf("no keys: %v", err)
	}
	if err := Verify([]ed25519.PublicKey{pub}, msg, []byte("junk")); err == nil {
		t.Fatal("verified junk")
	}
	// A flipped signature byte.
	bad := bytes.Clone(sig)
	bad[len(bad)-40] ^= 1
	if err := Verify([]ed25519.PublicKey{pub}, msg, bad); err == nil {
		t.Fatal("verified a corrupted signature")
	}
}

func TestParseAllowedSigners(t *testing.T) {
	pub, _ := newKey(t)
	other, _ := newKey(t)
	file := "# comment\n\n" + AuthorizedKey(pub, "release 2026") + "\n" +
		strings.Replace(AuthorizedKey(other, ""), Namespace+" ", "someone-else ", 1) + "\n" +
		strings.Replace(AuthorizedKey(other, ""), `namespaces="tw-release"`, `namespaces="git"`, 1) + "\n"
	keys, err := ParseAllowedSigners([]byte(file))
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || !keys[0].Equal(pub) {
		t.Fatalf("keys = %v", keys)
	}
	if _, err := ParseAllowedSigners([]byte(Namespace + " ssh-rsa AAAA\n")); err == nil {
		t.Fatal("accepted an rsa key")
	}
	if _, err := Pinned(); err != nil {
		t.Fatalf("the pinned allowed_signers: %v", err)
	}
}

// The signature ssh-keygen writes verifies here, and Sign's verifies with
// ssh-keygen: the shell installers and tw-hostd agree.
func TestSSHKeygenInterop(t *testing.T) {
	kg, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("needs ssh-keygen")
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	if out, err := exec.Command(kg, "-q", "-t", "ed25519", "-N", "", "-C", "test", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("keygen: %v %s", err, out)
	}
	pubLine, _ := os.ReadFile(key + ".pub")
	signers := filepath.Join(dir, "allowed_signers")
	os.WriteFile(signers, []byte(Namespace+` namespaces="`+Namespace+`" `+string(pubLine)), 0o600)
	keys, err := ParseAllowedSigners(mustRead(t, signers))
	if err != nil || len(keys) != 1 {
		t.Fatalf("parse ssh-keygen's key: %v %v", keys, err)
	}
	msg := filepath.Join(dir, "SHA256SUMS")
	os.WriteFile(msg, []byte("0000  a\n"), 0o600)
	if out, err := exec.Command(kg, "-Y", "sign", "-f", key, "-n", Namespace, msg).CombinedOutput(); err != nil {
		t.Fatalf("sign: %v %s", err, out)
	}
	if err := Verify(keys, mustRead(t, msg), mustRead(t, msg+".sig")); err != nil {
		t.Fatalf("ssh-keygen's signature: %v", err)
	}
	// The other way: a signature made here verifies with ssh-keygen.
	pub, priv := newKey(t)
	os.WriteFile(signers, []byte(AuthorizedKey(pub, "")+"\n"), 0o600)
	os.WriteFile(msg+".sig", Sign(priv, mustRead(t, msg)), 0o600)
	cmd := exec.Command(kg, "-Y", "verify", "-f", signers, "-I", Namespace, "-n", Namespace, "-s", msg+".sig")
	cmd.Stdin = bytes.NewReader(mustRead(t, msg))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen -Y verify of Sign's signature: %v %s", err, out)
	}
}

func TestVerifyTree(t *testing.T) {
	pub, priv := newKey(t)
	keys := []ed25519.PublicKey{pub}
	mk := func(t *testing.T) string {
		dir := t.TempDir()
		files := map[string]string{"install.sh": "#!/bin/sh\n", "VERSION": "0.3.0\n", "bin/tw": "tw", "share/tailworks/release.json": "{}"}
		var sums strings.Builder
		for _, n := range []string{"VERSION", "bin/tw", "install.sh", "share/tailworks/release.json"} {
			p := filepath.Join(dir, n)
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, []byte(files[n]), 0o644)
			s := sha256.Sum256([]byte(files[n]))
			sums.WriteString(hex.EncodeToString(s[:]) + "  " + n + "\n")
		}
		os.WriteFile(filepath.Join(dir, SumsFile), []byte(sums.String()), 0o644)
		os.WriteFile(filepath.Join(dir, SigFile), Sign(priv, []byte(sums.String())), 0o644)
		return dir
	}
	if err := VerifyTree(mk(t), keys); err != nil {
		t.Fatal(err)
	}
	for name, tamper := range map[string]func(dir string){
		"changed file":  func(d string) { os.WriteFile(filepath.Join(d, "bin/tw"), []byte("evil"), 0o755) },
		"unlisted file": func(d string) { os.WriteFile(filepath.Join(d, "bin/extra"), []byte("evil"), 0o755) },
		"missing file":  func(d string) { os.Remove(filepath.Join(d, "VERSION")) },
		"no signature":  func(d string) { os.Remove(filepath.Join(d, SigFile)) },
		"resigned sums": func(d string) {
			_, p := newKey(t)
			os.WriteFile(filepath.Join(d, SigFile), Sign(p, mustRead(t, filepath.Join(d, SumsFile))), 0o644)
		},
		"edited sums": func(d string) {
			f, _ := os.OpenFile(filepath.Join(d, SumsFile), os.O_APPEND|os.O_WRONLY, 0)
			f.WriteString("\n")
			f.Close()
		},
		"escaping entry": func(d string) {
			os.WriteFile(filepath.Join(d, SumsFile), []byte(strings.Repeat("0", 64)+"  ../x\n"), 0o644)
		},
	} {
		d := mk(t)
		tamper(d)
		if err := VerifyTree(d, keys); err == nil {
			t.Errorf("%s: verified", name)
		}
	}
}

// scripts/get.sh and scripts/install.sh pin the same keys as allowed_signers.
func TestScriptsPinTheSameKeys(t *testing.T) {
	want := keyLines(string(pinned))
	for _, s := range []string{"../../scripts/get.sh", "../../scripts/install.sh"} {
		b := string(mustRead(t, s))
		_, rest, ok := strings.Cut(b, "# BEGIN tw release signers")
		block, _, ok2 := strings.Cut(rest, "# END tw release signers")
		if !ok || !ok2 {
			t.Fatalf("%s: no BEGIN/END tw release signers block", s)
		}
		if got := keyLines(block); got != want {
			t.Errorf("%s pins %q, internal/releasesig/allowed_signers %q: run sh scripts/release-keygen.sh --sync", s, got, want)
		}
	}
}

func keyLines(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") && strings.HasPrefix(l, Namespace+" ") {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
