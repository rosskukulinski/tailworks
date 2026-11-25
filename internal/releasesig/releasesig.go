// Package releasesig verifies tw release signatures (issue #71).
//
// A release's SHA256SUMS is signed with an SSH signature (the format
// `ssh-keygen -Y sign` writes, OpenSSH's PROTOCOL.sshsig) by an Ed25519 key,
// in the namespace "tw-release". The public keys are pinned in this
// package (allowed_signers, embedded), in scripts/get.sh and in
// scripts/install.sh, never taken from the release or from tw-mgmt. SSH
// signatures verify offline with a tool every macOS and Linux host already
// has (ssh-keygen), which is why they were picked over minisign or Sigstore.
//
// Only Ed25519 keys are accepted.
package releasesig

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/sha512"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Namespace is the ssh-keygen -n namespace of a tailworks release signature, and
// the principal of its allowed_signers lines.
const Namespace = "tw-release"

// SumsFile and SigFile are the checksum list and its signature, at the root
// of a release tarball (and of a GitHub release's assets).
const (
	SumsFile = "SHA256SUMS"
	SigFile  = "SHA256SUMS.sig"
)

//go:embed allowed_signers
var pinned []byte

// PinnedFile is the embedded allowed_signers file.
func PinnedFile() []byte { return bytes.Clone(pinned) }

// Pinned is the release signing keys this binary trusts.
func Pinned() ([]ed25519.PublicKey, error) { return ParseAllowedSigners(pinned) }

// ParseAllowedSigners reads ssh-keygen allowed_signers lines for the
// tw-release principal (with no namespaces option, or one that includes
// tw-release). Other principals, comments and blank lines are skipped.
func ParseAllowedSigners(b []byte) ([]ed25519.PublicKey, error) {
	var keys []ed25519.PublicKey
	sc := bufio.NewScanner(bytes.NewReader(b))
	for n := 1; sc.Scan(); n++ {
		f := strings.Fields(sc.Text())
		if len(f) == 0 || strings.HasPrefix(f[0], "#") {
			continue
		}
		if !principalMatches(f[0]) {
			continue
		}
		i := 1
		ok := true
		if i < len(f) && f[i] != "ssh-ed25519" {
			ok = optionsAllow(f[i])
			i++
		}
		if i+1 >= len(f) || f[i] != "ssh-ed25519" {
			return nil, fmt.Errorf("allowed_signers line %d: want an ssh-ed25519 key", n)
		}
		if !ok {
			continue
		}
		blob, err := base64.StdEncoding.DecodeString(f[i+1])
		if err != nil {
			return nil, fmt.Errorf("allowed_signers line %d: %w", n, err)
		}
		k, err := parseEd25519(blob)
		if err != nil {
			return nil, fmt.Errorf("allowed_signers line %d: %w", n, err)
		}
		keys = append(keys, k)
	}
	return keys, sc.Err()
}

func principalMatches(p string) bool {
	for _, s := range strings.Split(p, ",") {
		if s == Namespace || s == "*" {
			return true
		}
	}
	return false
}

func optionsAllow(opts string) bool {
	for _, o := range strings.Split(opts, ",") {
		if v, ok := strings.CutPrefix(o, "namespaces="); ok {
			for _, ns := range strings.Split(strings.Trim(v, `"`), ",") {
				if ns == Namespace || ns == "*" {
					return true
				}
			}
			return false
		}
	}
	return true
}

// AuthorizedKey is k as an allowed_signers line.
func AuthorizedKey(k ed25519.PublicKey, comment string) string {
	line := Namespace + ` namespaces="` + Namespace + `" ssh-ed25519 ` + base64.StdEncoding.EncodeToString(keyBlob(k))
	if comment != "" {
		line += " " + comment
	}
	return line
}

// Sign returns an armored SSH signature of msg (what
// `ssh-keygen -Y sign -n tw-release` writes). Releases are signed with
// ssh-keygen; this is for tests.
func Sign(priv ed25519.PrivateKey, msg []byte) []byte {
	h := sha512.Sum512(msg)
	sig := ed25519.Sign(priv, signedData("sha512", h[:]))
	var b []byte
	b = append(b, "SSHSIG"...)
	b = binary.BigEndian.AppendUint32(b, 1)
	b = appendString(b, keyBlob(priv.Public().(ed25519.PublicKey)))
	b = appendString(b, []byte(Namespace))
	b = appendString(b, nil)
	b = appendString(b, []byte("sha512"))
	b = appendString(b, appendString(appendString(nil, []byte("ssh-ed25519")), sig))
	return pem.EncodeToMemory(&pem.Block{Type: "SSH SIGNATURE", Bytes: b})
}

// ErrNoKeys is Verify's error when no signing key is pinned.
var ErrNoKeys = errors.New("no release signing key is pinned")

// ErrUnsigned is VerifyTree's error (wrapped) when the release carries no
// SHA256SUMS or no SHA256SUMS.sig at all, as opposed to a signature that
// doesn't verify.
var ErrUnsigned = errors.New("release is not signed")

// Verify checks that armored is a tw-release SSH signature of msg by one of
// keys.
func Verify(keys []ed25519.PublicKey, msg, armored []byte) error {
	if len(keys) == 0 {
		return ErrNoKeys
	}
	blk, _ := pem.Decode(armored)
	if blk == nil || blk.Type != "SSH SIGNATURE" {
		return errors.New("not an armored SSH signature")
	}
	r := reader(blk.Bytes)
	if magic := r.raw(6); string(magic) != "SSHSIG" {
		return errors.New("not an SSH signature (no SSHSIG preamble)")
	}
	if v := r.uint32(); v != 1 {
		return fmt.Errorf("SSH signature version %d", v)
	}
	pubBlob := r.str()
	ns := r.str()
	reserved := r.str()
	hashAlg := r.str()
	sigBlob := r.str()
	if r.err != nil {
		return fmt.Errorf("SSH signature: %w", r.err)
	}
	if string(ns) != Namespace {
		return fmt.Errorf("signature namespace %q, want %q", ns, Namespace)
	}
	pub, err := parseEd25519(pubBlob)
	if err != nil {
		return err
	}
	trusted := false
	for _, k := range keys {
		if k.Equal(pub) {
			trusted = true
		}
	}
	if !trusted {
		return fmt.Errorf("signed by %s, which is not a pinned release key", fingerprint(pub))
	}
	sr := reader(sigBlob)
	format, sig := sr.str(), sr.str()
	if sr.err != nil || string(format) != "ssh-ed25519" || len(sig) != ed25519.SignatureSize {
		return errors.New("SSH signature: not an ssh-ed25519 signature")
	}
	var h hash.Hash
	switch string(hashAlg) {
	case "sha512":
		h = sha512.New()
	case "sha256":
		h = sha256.New()
	default:
		return fmt.Errorf("SSH signature: hash %q", hashAlg)
	}
	h.Write(msg)
	data := signedDataReserved(string(hashAlg), reserved, h.Sum(nil))
	if !ed25519.Verify(pub, data, sig) {
		return errors.New("bad signature")
	}
	return nil
}

// VerifyTree checks an extracted release directory: SHA256SUMS.sig is a
// signature of SHA256SUMS by one of keys, every file SHA256SUMS lists has
// that hash, and every regular file in dir is listed (but SHA256SUMS and
// its signature). Symlinks need not be listed (extractTarGz keeps them in
// the tree, and the files they point at are checked).
func VerifyTree(dir string, keys []ed25519.PublicKey) error {
	sums, err := os.ReadFile(filepath.Join(dir, SumsFile))
	if err != nil {
		return fmt.Errorf("%w: release has no %s", ErrUnsigned, SumsFile)
	}
	sig, err := os.ReadFile(filepath.Join(dir, SigFile))
	if err != nil {
		return fmt.Errorf("%w (no %s)", ErrUnsigned, SigFile)
	}
	if err := Verify(keys, sums, sig); err != nil {
		return fmt.Errorf("%s: %w", SigFile, err)
	}
	listed := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		sum, name, ok := strings.Cut(line, "  ")
		if !ok || len(sum) != 64 {
			return fmt.Errorf("%s: bad line %q", SumsFile, line)
		}
		name = strings.TrimPrefix(name, "./")
		if !filepath.IsLocal(name) {
			return fmt.Errorf("%s: path %q escapes the release", SumsFile, name)
		}
		listed[filepath.ToSlash(filepath.Clean(name))] = sum
	}
	if err := sc.Err(); err != nil {
		return err
	}
	seen := 0
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if rel == SumsFile || rel == SigFile {
			return nil
		}
		want, ok := listed[rel]
		if !ok {
			return fmt.Errorf("%s is not in the signed %s", rel, SumsFile)
		}
		got, err := fileSHA256(p)
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("%s: SHA-256 %s, want %s", rel, got, want)
		}
		seen++
		return nil
	})
	if err != nil {
		return err
	}
	if seen != len(listed) {
		return fmt.Errorf("%d files listed in %s are missing", len(listed)-seen, SumsFile)
	}
	return nil
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fingerprint(k ed25519.PublicKey) string {
	s := sha256.Sum256(keyBlob(k))
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(s[:])
}

func signedData(hashAlg string, digest []byte) []byte {
	return signedDataReserved(hashAlg, nil, digest)
}

func signedDataReserved(hashAlg string, reserved, digest []byte) []byte {
	var b []byte
	b = append(b, "SSHSIG"...)
	b = appendString(b, []byte(Namespace))
	b = appendString(b, reserved)
	b = appendString(b, []byte(hashAlg))
	return appendString(b, digest)
}

func keyBlob(k ed25519.PublicKey) []byte {
	return appendString(appendString(nil, []byte("ssh-ed25519")), k)
}

func parseEd25519(blob []byte) (ed25519.PublicKey, error) {
	r := reader(blob)
	typ, key := r.str(), r.str()
	if r.err != nil || string(typ) != "ssh-ed25519" || len(key) != ed25519.PublicKeySize || len(r.b) != 0 {
		return nil, fmt.Errorf("not an ssh-ed25519 public key")
	}
	return ed25519.PublicKey(key), nil
}

func appendString(b, s []byte) []byte {
	b = binary.BigEndian.AppendUint32(b, uint32(len(s)))
	return append(b, s...)
}

type wire struct {
	b   []byte
	err error
}

func reader(b []byte) *wire { return &wire{b: b} }

func (w *wire) raw(n int) []byte {
	if w.err != nil || len(w.b) < n {
		w.err = errors.New("truncated")
		return nil
	}
	v := w.b[:n]
	w.b = w.b[n:]
	return v
}

func (w *wire) uint32() uint32 {
	b := w.raw(4)
	if b == nil {
		return 0
	}
	return binary.BigEndian.Uint32(b)
}

func (w *wire) str() []byte {
	n := w.uint32()
	if w.err != nil || uint64(n) > uint64(len(w.b)) {
		w.err = errors.New("truncated")
		return nil
	}
	return w.raw(int(n))
}
