package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// appWorkloadCmds are the data commands that take the workload from the
// app in the current directory (issue #524).
var appWorkloadCmds = []struct {
	name string
	run  func() error
}{
	{"d1 ls", func() error { return cmdD1([]string{"ls"}) }},
	{"kv namespace ls", func() error { return cmdKV([]string{"namespace", "ls"}) }},
	{"kv key ls", func() error { return cmdKV([]string{"key", "ls", "--namespace", "NS"}) }},
	{"r2 bucket ls", func() error { return cmdR2([]string{"bucket", "ls"}) }},
	{"secret ls", func() error { return cmdSecret([]string{"ls"}) }},
}

// An app that fails to load reports loadApp's own error, not "no app".
func TestAppWorkloadSurfacesLoadError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tailworks.jsonc"), []byte(`{"name": "notes", "objcts": {}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "wrangler.jsonc"), []byte(`{"name": "notes", "main": "src/index.ts"}`), 0o644)
	t.Chdir(dir)
	for _, c := range appWorkloadCmds {
		err := c.run()
		txt := cliText(err)
		if cliCode(err) != "bad_request" || !strings.Contains(txt, "objcts") || strings.Contains(txt, "no app in the current directory") {
			t.Errorf("%s: %v\n%s", c.name, err, txt)
		}
		if !strings.Contains(txt, "tw "+c.name+" … --workload <workload>") || !strings.Contains(txt, "to skip the app in this directory") {
			t.Errorf("%s: no --workload fix:\n%s", c.name, txt)
		}
	}
}

// With no app config at all, the error stays "no app in the current directory".
func TestAppWorkloadNoApp(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, c := range appWorkloadCmds {
		err := c.run()
		txt := cliText(err)
		if cliCode(err) != "bad_request" || !strings.Contains(txt, "no app in the current directory") || !strings.Contains(txt, "--workload <workload>") {
			t.Errorf("%s: %v\n%s", c.name, err, txt)
		}
	}
}

func TestAppBaseName(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ tw, wr, want string }{
		{"a", "b", "a"},
		{"", "b", "b"},
		{"", "", "dir"},
	} {
		if got := appBaseName(c.tw, c.wr, "/x/dir"); got != c.want {
			t.Errorf("appBaseName(%q, %q) = %q, want %q", c.tw, c.wr, got, c.want)
		}
	}
}
