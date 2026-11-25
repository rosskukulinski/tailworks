package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/schema"
)

// TestCompletionTree: the tree behind `tw completion` has every command,
// the subcommands of the usage lines (two and three words deep), and the
// flags of each command's flag set.
func TestCompletionTree(t *testing.T) {
	tr := buildCompletionTree()
	root := tr.nodes[""]
	for _, c := range commands {
		if c.hidden {
			if slices.Contains(root.subs, c.name) {
				t.Errorf("completion offers the hidden command %s", c.name)
			}
			continue
		}
		if !slices.Contains(root.subs, c.name) {
			t.Errorf("completion lacks the command %s", c.name)
		}
		// Every command completes some flag, at its own level or below.
		n := 0
		for p, nd := range tr.nodes {
			if p == c.name || strings.HasPrefix(p, c.name+" ") {
				n += len(nd.flags)
			}
		}
		if n == 0 {
			t.Errorf("completion has no flags for %s", c.name)
		}
	}
	for path, subs := range map[string][]string{
		"kv":            {"namespace", "key"},
		"kv key":        {"put", "get", "rm", "ls"},
		"d1":            {"create", "execute", "migrations"},
		"d1 migrations": {"create", "ls", "apply"},
		"r2 bucket":     {"ls", "create", "rm"},
		"image public":  {"add", "ls", "rm"},
		"host":          {"add", "ls", "rm", "setup", "vmcheck"},
		"tenant":        {"add", "ls", "set", "rm"},
		"secret":        {"put", "ls", "rm"},
		"queues":        {"create", "send", "messages"},
		"expose":        {"on", "off", "names", "release"},
		"completion":    {"bash", "zsh", "fish"},
		"docs":          {"search", "show", "ls"},
		"agent":         {"setup", "remove", "ls", "show"},
	} {
		n := tr.nodes[path]
		if n == nil {
			t.Errorf("completion has no path %q", path)
			continue
		}
		for _, s := range subs {
			if !slices.Contains(n.subs, s) {
				t.Errorf("completion %q lacks %q (has %v)", path, s, n.subs)
			}
		}
	}
	for path, flags := range map[string][]string{
		"":                    {"--help", "--json", "--state-dir"},
		"deploy":              {"--replace", "--tenant", "--json", "--help"},
		"kv key put":          {"--ttl", "--metadata", "--binding", "--workload"},
		"d1 migrations apply": {"--workload", "--tenant"},
		"logs":                {"-n", "-f", "--status"},
		"tenant add":          {"--oauth-client-file", "--api-key-file", "--apply-policy", "--control"},
		"host setup":          {"--microvm", "--undo", "--hugepages"},
		"version":             {"--check"},
	} {
		n := tr.nodes[path]
		if n == nil {
			t.Errorf("completion has no path %q", path)
			continue
		}
		for _, fl := range flags {
			if !n.flags[fl] {
				t.Errorf("completion %q lacks flag %s (has %v)", path, fl, tr.flagList(path))
			}
		}
	}
	if !tr.valFlags["--tenant"] || !tr.valFlags["--state-dir"] || tr.valFlags["--json"] || tr.valFlags["--yes"] {
		t.Errorf("value flags: %v", tr.valFlagList())
	}
}

// TestCompletionScripts: each shell's script names every command, parses
// (bash -n, zsh -n, fish -n where installed), and --json wraps it.
func TestCompletionScripts(t *testing.T) {
	tr := buildCompletionTree()
	for _, sh := range completionShells {
		var script string
		switch sh {
		case "bash":
			script = tr.bash()
		case "zsh":
			script = tr.zsh()
		case "fish":
			script = tr.fish()
		}
		for _, c := range commands {
			if !c.hidden && !strings.Contains(script, c.name) {
				t.Errorf("%s completion lacks %s", sh, c.name)
			}
		}
		for _, w := range []string{"migrations", "namespace", "--ttl"} {
			if !strings.Contains(script, w) {
				t.Errorf("%s completion lacks %s", sh, w)
			}
		}
		if bin, err := exec.LookPath(sh); err == nil {
			f := filepath.Join(t.TempDir(), "tw."+sh)
			os.WriteFile(f, []byte(script), 0o644)
			if out, err := exec.Command(bin, "-n", f).CombinedOutput(); err != nil {
				t.Errorf("%s -n: %v\n%s", sh, err, out)
			}
		}

		out, err := captureStdout(t, func() error { return cmdCompletion([]string{sh, "--json"}) })
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(out), &v); err != nil || v["shell"] != sh || v["script"] != script {
			t.Fatalf("completion %s --json: %v %.200q", sh, err, out)
		}
		if probs, err := schema.CheckCLI("completion", v); err != nil || len(probs) > 0 {
			t.Fatalf("completion %s --json vs schema: %v %v", sh, probs, err)
		}
	}
	if err := cmdCompletion([]string{"powershell"}); err == nil || !strings.Contains(err.Error(), "bad_request") {
		t.Errorf("completion powershell: %v", err)
	}
}

// TestCompletionBash runs the bash script's completion function for a few
// command lines.
func TestCompletionBash(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "tw.bash"), []byte(buildCompletionTree().bash()), 0o644)
	driver := `source "$1"
c() { COMP_WORDS=("$@"); COMP_CWORD=$((${#COMP_WORDS[@]} - 1)); COMPREPLY=(); _tw; echo "<${COMPREPLY[*]}>"; }
c tw ""
c tw ku
c tw kv ""
c tw kv key ""
c tw kv key put --tt
c tw --state-dir /x d1 migrations a
c tw expose ""
c tw logs myapp ""
c tw deploy --tenant foo --repl
c tw deploy --tenant ""
`
	os.WriteFile(filepath.Join(dir, "driver.sh"), []byte(driver), 0o644)
	out, err := exec.Command(bash, filepath.Join(dir, "driver.sh"), filepath.Join(dir, "tw.bash")).CombinedOutput()
	if err != nil {
		t.Fatalf("bash: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	want := []func(string) bool{
		func(l string) bool {
			return strings.Contains(l, "deploy") && strings.Contains(l, "whoami") && strings.Contains(l, "completion")
		},
		func(l string) bool { return l == "" }, // nothing starts with "ku"
		func(l string) bool { return l == "namespace key" },
		func(l string) bool { return l == "put get rm ls" },
		func(l string) bool { return l == "--ttl" },
		func(l string) bool { return l == "apply" },
		func(l string) bool { return l == "on off names release" },
		func(l string) bool { return l == "" }, // a positional: files (complete -o default)
		func(l string) bool { return l == "--replace" },
		func(l string) bool { return l == "" }, // --tenant's value
	}
	if len(lines) != len(want) {
		t.Fatalf("bash completion: %d lines, want %d:\n%s", len(lines), len(want), out)
	}
	for i, ok := range want {
		if l := strings.TrimSuffix(strings.TrimPrefix(lines[i], "<"), ">"); !ok(l) {
			t.Errorf("bash completion line %d: %q", i, lines[i])
		}
	}
}
