//go:build e2etwupgrade

package twupgrade

import (
	"archive/tar"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/internal/releasesig"
)

const vpkg = "github.com/rosskukulinski/tailworks/internal/version"

// buildBins compiles tw, tw-mgmt and tw-hostd from this tree, stamped v
// (as scripts/release.sh stamps a release).
func buildBins(t *testing.T, root, v string) string {
	t.Helper()
	out := t.TempDir()
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-X "+vpkg+".Version="+v, "-o", out+"/", "./cmd/tw", "./cmd/tw-mgmt", "./cmd/tw-hostd")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build (%s): %v\n%s", v, err, b)
	}
	return out
}

// release describes one tarball. The layout is scripts/release.sh's
// (bin/, share/tailworks/release.json, install.sh, VERSION, SHA256SUMS,
// SHA256SUMS.sig), minus what tw upgrade doesn't look at: the templates
// and vendored modules (TW_CREATE_TAILWORKS points at this tree's) and
// workerd, which is a stub that answers --version for install.sh's smoke
// test (TW_WORKERD is the real one, pinned by make deps).
type release struct {
	Version string             // VERSION and release.json
	Bins    string             // tw, tw-mgmt, tw-hostd
	Key     ed25519.PrivateKey // signs SHA256SUMS; nil = unsigned
	// BrokenUp: bin/tw is a wrapper whose `tw up` fails (a release that
	// doesn't start); everything else (version, down) works.
	BrokenUp bool
	// Sentinel, if set, is a file install.sh creates first thing: the
	// refused releases carry it, to prove install.sh never ran.
	Sentinel string
	// TamperAfterSigning adds the sentinel line to install.sh after
	// SHA256SUMS was signed (the signature still verifies, the file
	// doesn't match it).
	TamperAfterSigning bool
}

// build writes dir/tailworks-<v>-<os>-<arch>.tar.gz and dir/SHA256SUMS
// (what a GitHub release has next to it) and returns the tarball.
func (r release) build(t *testing.T, root, dir string) string {
	t.Helper()
	name := fmt.Sprintf("tailworks-%s-%s-%s", r.Version, runtime.GOOS, runtime.GOARCH)
	st := filepath.Join(t.TempDir(), name)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(st, "bin"), 0o755))
	must(os.MkdirAll(filepath.Join(st, "share", "tailworks"), 0o755))
	for _, b := range []string{"tw", "tw-mgmt", "tw-hostd"} {
		dst := b
		if b == "tw" && r.BrokenUp {
			dst = "tw.real"
		}
		must(copyFile(filepath.Join(r.Bins, b), filepath.Join(st, "bin", dst), 0o755))
	}
	if r.BrokenUp {
		must(os.WriteFile(filepath.Join(st, "bin", "tw"), []byte(`#!/bin/sh
# e2e/twupgrade: a release that doesn't start.
for a in "$@"; do
	if [ "$a" = up ]; then echo "tw: this release is broken on purpose (e2e/twupgrade)" >&2; exit 1; fi
done
exec "$(dirname "$0")/tw.real" "$@"
`), 0o755))
	}
	must(os.WriteFile(filepath.Join(st, "bin", "workerd"), []byte("#!/bin/sh\necho 'workerd (e2e/twupgrade stub; TW_WORKERD is the real one)'\n"), 0o755))
	must(os.WriteFile(filepath.Join(st, "VERSION"), []byte(r.Version+"\n"), 0o644))
	must(os.WriteFile(filepath.Join(st, "share", "tailworks", "release.json"),
		[]byte(fmt.Sprintf(`{"version":%q,"os":%q,"arch":%q,"workerd":"","microvm":false}`+"\n", r.Version, runtime.GOOS, runtime.GOARCH)), 0o644))
	inst, err := os.ReadFile(filepath.Join(root, "scripts", "install.sh"))
	must(err)
	sentinelLine := func(b []byte) []byte {
		return []byte(strings.Replace(string(b), "set -eu\n", "set -eu\ntouch '"+r.Sentinel+"'\n", 1))
	}
	if r.Sentinel != "" && !r.TamperAfterSigning {
		inst = sentinelLine(inst)
	}
	must(os.WriteFile(filepath.Join(st, "install.sh"), inst, 0o755))

	sums := treeSums(t, st)
	must(os.WriteFile(filepath.Join(st, releasesig.SumsFile), sums, 0o644))
	if r.Key != nil {
		must(os.WriteFile(filepath.Join(st, releasesig.SigFile), releasesig.Sign(r.Key, sums), 0o644))
	}
	if r.TamperAfterSigning {
		must(os.WriteFile(filepath.Join(st, "install.sh"), sentinelLine(inst), 0o755))
	}

	must(os.MkdirAll(dir, 0o755))
	tgz := filepath.Join(dir, name+".tar.gz")
	must(writeTarGz(tgz, filepath.Dir(st), name))
	sum, err := fileSum(tgz)
	must(err)
	must(os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sum+"  "+filepath.Base(tgz)+"\n"), 0o644))
	return tgz
}

// treeSums is release.sh's SHA256SUMS: every file but SHA256SUMS, sorted.
func treeSums(t *testing.T, dir string) []byte {
	t.Helper()
	var names []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			rel, _ := filepath.Rel(dir, p)
			if rel != releasesig.SumsFile && rel != releasesig.SigFile {
				names = append(names, filepath.ToSlash(rel))
			}
		}
		return err
	})
	slices.Sort(names)
	var b strings.Builder
	for _, n := range names {
		s, err := fileSum(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		b.WriteString(s + "  " + n + "\n")
	}
	return []byte(b.String())
}

func fileSum(p string) (string, error) {
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

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// writeTarGz archives base/name (directories and regular files) into tgz.
func writeTarGz(tgz, base, name string) error {
	f, err := os.Create(tgz)
	if err != nil {
		return err
	}
	defer f.Close()
	zw, _ := gzip.NewWriterLevel(f, gzip.BestSpeed)
	tw := tar.NewWriter(zw)
	err = filepath.WalkDir(filepath.Join(base, name), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(base, p)
		hd, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hd.Name = filepath.ToSlash(rel)
		if d.IsDir() {
			hd.Name += "/"
		}
		if err := tw.WriteHeader(hd); err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(tw, in)
		return err
	})
	if err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return f.Close()
}
