package main

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCreateTailworksRenamesLikeInit: `npm create tailworks` without tw on
// PATH (js/packages/create-tailworks/lib/init.mjs) scaffolds every template
// exactly as `tw init` does: same files, same workload name, same tags.
func TestCreateTailworksRenamesLikeInit(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	ct, err := filepath.Abs(filepath.Join("..", "..", "js", "packages", "create-tailworks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TW_CREATE_TAILWORKS", ct)
	// A PATH with node and nothing else, so create-tailworks can't find tw.
	bin := t.TempDir()
	if err := os.Symlink(node, filepath.Join(bin, "node")); err != nil {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(filepath.Join(ct, "templates"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		tmpl := e.Name()
		// Upper case and an underscore exercise workloadLabel; İ (U+0130)
		// is the one letter Go and JavaScript lowercase differently.
		for dir, label := range map[string]string{"My_App-2": "my-app-2", "İstanbul_App": "istanbul-app"} {
			t.Run(tmpl+"/"+label, func(t *testing.T) {
				want := filepath.Join(t.TempDir(), dir)
				if _, err := captureStdout(t, func() error { return cmdInit([]string{want, "--template", tmpl}) }); err != nil {
					t.Fatal(err)
				}
				got := filepath.Join(t.TempDir(), dir)
				cmd := exec.Command(node, filepath.Join(ct, "bin", "create-tailworks.js"), got, "--template", tmpl)
				cmd.Env = append(os.Environ(), "PATH="+bin)
				out, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("create-tailworks: %v\n%s", err, out)
				}
				if !strings.Contains(string(out), `(workload "`+label+`")`) {
					t.Errorf("create-tailworks output lacks the workload name %q:\n%s", label, out)
				}
				createTwCompareTrees(t, want, got)
			})
		}
	}
}

// createTwCompareTrees fails t unless the two directories hold the same
// files with the same bytes.
func createTwCompareTrees(t *testing.T, want, got string) {
	t.Helper()
	files := map[string]bool{}
	for _, root := range []string{want, got} {
		filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel(root, p)
				files[rel] = true
			}
			return nil
		})
	}
	for rel := range files {
		w, werr := os.ReadFile(filepath.Join(want, rel))
		g, gerr := os.ReadFile(filepath.Join(got, rel))
		switch {
		case werr != nil:
			t.Errorf("%s: only create-tailworks wrote it", rel)
		case gerr != nil:
			t.Errorf("%s: only tw init wrote it", rel)
		case !bytes.Equal(w, g):
			t.Errorf("%s differs:\n--- tw init\n%s\n--- create-tailworks\n%s", rel, w, g)
		}
	}
}
