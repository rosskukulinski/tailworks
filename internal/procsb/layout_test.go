package procsb

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckLayout pins the layout check behind sandbox_layout_unsafe (issue
// #342) on every OS: a tailworks path that is a sandbox-readable tree, or
// lies below one directly, through a symlink, or as a not-yet-created child,
// is refused naming the path, its role and the tree; a path outside every
// tree is accepted.
func TestCheckLayout(t *testing.T) {
	root := t.TempDir()
	root, err := filepath.EvalSymlinks(root) // macOS: /var → /private/var
	if err != nil {
		t.Fatal(err)
	}
	opt := filepath.Join(root, "opt")
	home := filepath.Join(root, "home", "tw")
	for _, d := range []string{filepath.Join(opt, "tailworks", "etc"), home} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cred := filepath.Join(opt, "tailworks", "etc", "oauth.env")
	if err := os.WriteFile(cred, []byte("TS_OAUTH_CLIENT_SECRET=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A state dir outside the tree that is a symlink into it.
	linked := filepath.Join(root, "var-lib-tw")
	if err := os.Symlink(filepath.Join(opt, "tailworks", "state"), linked); err != nil {
		t.Fatal(err)
	}
	// A tree named through a symlink (/lib → /usr/lib on merged-usr systems).
	optLink := filepath.Join(root, "opt-link")
	if err := os.Symlink(opt, optLink); err != nil {
		t.Fatal(err)
	}
	trees := []string{opt, filepath.Join(root, "usr"), ""}

	cases := []struct {
		name string
		path LayoutPath
		tree string // "" = accepted
	}{
		{"state dir under the tree, not created yet", LayoutPath{"state dir", filepath.Join(opt, "tailworks", ".tailworks")}, opt},
		{"credential file under the tree", LayoutPath{"credential file", cred}, opt},
		{"the tree itself", LayoutPath{"state dir", opt}, opt},
		{"symlink into the tree", LayoutPath{"state dir", filepath.Join(linked, "mgmt")}, opt},
		{"a tree that doesn't exist", LayoutPath{"run dir", filepath.Join(root, "usr", "local", "tw")}, filepath.Join(root, "usr")},
		{"home dir outside", LayoutPath{"home dir", home}, ""},
		{"sibling with the tree's name as a prefix", LayoutPath{"state dir", opt + "-data"}, ""},
		{"empty path", LayoutPath{"credential file", ""}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckLayout([]LayoutPath{c.path}, trees)
			if c.tree == "" {
				if err != nil {
					t.Fatalf("accepted layout refused: %v", err)
				}
				return
			}
			var le *LayoutError
			if !errors.As(err, &le) {
				t.Fatalf("got %v, want *LayoutError", err)
			}
			if le.Tree != c.tree || le.Path != c.path.Path || le.What != c.path.What {
				t.Fatalf("LayoutError = %+v, want tree %s, path %s, what %s", le, c.tree, c.path.Path, c.path.What)
			}
			for _, want := range []string{LayoutErrorCode, c.path.What, c.path.Path, c.tree} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q lacks %q", err, want)
				}
			}
		})
	}

	// The tree given through a symlink still catches a path below the
	// real directory, and the first offending path is reported.
	err = CheckLayout([]LayoutPath{{"home dir", home}, {"run dir", filepath.Join(opt, "run")}, {"state dir", cred}}, []string{optLink})
	var le *LayoutError
	if !errors.As(err, &le) || le.What != "run dir" || le.Tree != optLink {
		t.Fatalf("symlinked tree: got %v", err)
	}
	if err := CheckLayout(nil, trees); err != nil {
		t.Fatal(err)
	}
}
