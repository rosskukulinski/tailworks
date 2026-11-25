package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/e2e/cliflow"
	"github.com/rosskukulinski/tailworks/hostd/hostdtest"
	"github.com/rosskukulinski/tailworks/netd/nettest"
)

func TestDevCallerFlag(t *testing.T) {
	t.Parallel()
	var c callerFlags
	for _, ok := range []string{"alice", "ci=tag:ci", "bot=tag:a,tag:b"} {
		if err := c.Set(ok); err != nil {
			t.Fatalf("--caller %s: %v", ok, err)
		}
	}
	if len(c) != 3 || c[1].name != "ci" || strings.Join(c[2].tags, " ") != "tag:a tag:b" || len(c[0].tags) != 0 {
		t.Fatalf("parsed %+v", c)
	}
	for _, bad := range []string{"", "you", "Alice", "a b", "x=ci", "x=tag:", "alice"} {
		if err := c.Set(bad); err == nil {
			t.Errorf("--caller %q: want an error", bad)
		}
	}
}

// TestDevCallers: `tw dev --caller` brings up one dev node per identity
// (a user's, a tagged one), each with its own local URL; the app's
// getCaller() sees each one, verified by netd.
func TestDevCallers(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	_, err := nettest.WorkerdBinary()
	if err != nil || !hostdtest.HasNode() {
		t.Skip("needs workerd and node (make deps)")
	}
	bin := cliflow.BuildBinaries(t)
	work := t.TempDir()
	app := filepath.Join(work, "hello")
	e := &cliflow.Env{T: t, BinDir: bin, StateDir: filepath.Join(work, "state"), WorkDir: work}
	e.MustJSON(nil, "init", app, "--template", "hello")
	var r devResult
	e.MustJSON(&r, "dev", app, "--once", "--port", "0", "--caller", "alice", "--caller", "ci=tag:ci")
	if len(r.Callers) != 2 {
		t.Fatalf("callers: %+v", r.Callers)
	}
	alice, ci := r.Callers[0], r.Callers[1]
	if alice.Name != "alice" || alice.As == "" || alice.As == r.As || alice.NodeID == "" || len(alice.Tags) != 0 || !strings.HasPrefix(alice.URL, "http://127.0.0.1:") {
		t.Fatalf("alice: %+v (default caller %s)", alice, r.As)
	}
	if ci.Name != "ci" || ci.As != "" || strings.Join(ci.Tags, ",") != "tag:ci" || ci.NodeID == "" || ci.NodeID == alice.NodeID {
		t.Fatalf("ci: %+v", ci)
	}
}

// TestTemplateTests runs each template's own `npm test` (a black-box test
// through @tailworks/tailnet/testing/dev, i.e. tw dev with --caller) against
// this build of tailworks, from a copy of the template outside the repo.
func TestTemplateTests(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("short")
	}
	_, err := nettest.WorkerdBinary()
	if err != nil || !hostdtest.HasNode() {
		t.Skip("needs workerd and node (make deps)")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("needs npm")
	}
	root, err := nettest.FindRepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := cliflow.BuildBinaries(t)
	for _, tm := range []string{"hello", "lock", "agent", "mcp", "ai-agent", "refund-approval", "static-site"} {
		t.Run(tm, func(t *testing.T) {
			src := filepath.Join(root, "js", "packages", "create-tailworks", "templates", tm)
			dir := filepath.Join(t.TempDir(), tm)
			if err := os.CopyFS(dir, os.DirFS(src)); err != nil {
				t.Fatal(err)
			}
			// The test imports @tailworks/tailnet/testing/dev: resolve it (and the
			// app's own deps) from the repo's workspace.
			if err := os.Symlink(filepath.Join(root, "node_modules"), filepath.Join(dir, "node_modules")); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("npm", "test", "--silent")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "TW_BIN="+filepath.Join(bin, "tw"))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("npm test in the %s template: %v\n%s", tm, err, out)
			}
		})
	}
}
