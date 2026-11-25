package bundle_test

import (
	"archive/tar"
	"bytes"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/hostd/bundle"
)

// TestUnpackRefusesHostileTars pins ledger claim C20 (bundle extraction
// rejects traversal, links and oversize bundles; no name can break out of
// the generated capnp config). bundle.Unpack is what hostd runs on every
// deployment's bundle (hostd/workload.go) before anything is written.
func TestUnpackRefusesHostileTars(t *testing.T) {
	mk := func(hs ...*tar.Header) []byte {
		t.Helper()
		var buf bytes.Buffer
		tw := tar.NewWriter(&buf)
		for _, h := range hs {
			if h.Typeflag == 0 {
				h.Typeflag = tar.TypeReg
			}
			if h.Mode == 0 {
				h.Mode = 0o644
			}
			if err := tw.WriteHeader(h); err != nil {
				t.Fatalf("%s: %v", h.Name, err)
			}
			if h.Typeflag == tar.TypeReg && h.Size > 0 {
				tw.Write(make([]byte, h.Size))
			}
		}
		tw.Close()
		return buf.Bytes()
	}
	reg := func(name string) *tar.Header { return &tar.Header{Name: name, Size: 1} }

	if files, err := bundle.Unpack(mk(reg("dist/index.js"), reg("./tailworks.jsonc"), &tar.Header{Name: "dist/", Typeflag: tar.TypeDir, Mode: 0o755})); err != nil || len(files) != 2 {
		t.Fatalf("a plain bundle: %v %v", files, err)
	}
	for name, h := range map[string]*tar.Header{
		"parent":           reg("../x.js"),
		"nested parent":    reg("dist/../../x.js"),
		"absolute":         reg("/etc/passwd"),
		"dot":              reg("."),
		"backslash":        reg(`dist\..\x.js`),
		"quote (capnp)":    reg(`a"),(name = "EVIL.js`),
		"newline":          reg("a\nb.js"),
		"carriage return":  reg("a\rb.js"),
		"symlink":          {Name: "dist/link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"},
		"hard link":        {Name: "dist/hard", Typeflag: tar.TypeLink, Linkname: "../../etc/passwd"},
		"character device": {Name: "dist/dev", Typeflag: tar.TypeChar},
		"fifo":             {Name: "dist/fifo", Typeflag: tar.TypeFifo},
	} {
		if files, err := bundle.Unpack(mk(reg("dist/index.js"), h)); err == nil {
			t.Errorf("%s (%q): accepted: %v", name, h.Name, keys(files))
		}
	}
	// Oversize: the running total of declared sizes is checked before a
	// byte is read, so a bundle just over the cap is refused.
	half := int64(bundle.MaxBundle/2 + 1)
	if _, err := bundle.Unpack(mk(&tar.Header{Name: "a.bin", Size: half}, &tar.Header{Name: "b.bin", Size: half})); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("oversize bundle: %v", err)
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
