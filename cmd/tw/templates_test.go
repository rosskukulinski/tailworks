package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestInitTemplatesTable: templates.go's table names exactly the templates
// in js/packages/create-tailworks/templates, and the init usage line lists
// its starting points.
func TestInitTemplatesTable(t *testing.T) {
	ents, err := os.ReadDir(filepath.Join("..", "..", "js", "packages", "create-tailworks", "templates"))
	if err != nil {
		t.Fatal(err)
	}
	var dir, table []string
	for _, e := range ents {
		if e.IsDir() {
			dir = append(dir, e.Name())
		}
	}
	for _, tm := range initTemplates {
		table = append(table, tm.Name)
		if tm.Purpose == "" {
			t.Errorf("%s: no purpose", tm.Name)
		}
	}
	slices.Sort(dir)
	slices.Sort(table)
	if !slices.Equal(dir, table) {
		t.Errorf("templates.go lists %v, the templates directory has %v", table, dir)
	}
	if want := "--template " + startingPoints("|") + "]"; !strings.Contains(usageOf("init"), want) {
		t.Errorf("init usage %q lacks %q", usageOf("init"), want)
	}
}

// TestInitList: `tw init --list` needs no directory, prints the starting
// points before the demos, and honours --json.
func TestInitList(t *testing.T) {
	out, err := captureStdout(t, func() error { return cmdInit([]string{"--list"}) })
	if err != nil {
		t.Fatal(err)
	}
	sp, demos := strings.Index(out, "Starting points:"), strings.Index(out, "Demos, patterns")
	if sp < 0 || demos < sp {
		t.Fatalf("sections missing or out of order:\n%s", out)
	}
	for _, tm := range initTemplates {
		i := strings.Index(out, "  "+tm.Name+" ")
		if i < 0 || (i > demos) == tm.StartingPoint {
			t.Errorf("%s: missing or in the wrong section:\n%s", tm.Name, out)
		}
	}
	out, err = captureStdout(t, func() error { return cmdInit([]string{"--list", "--json"}) })
	jsonOut = false
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Templates []initTemplate `json:"templates"`
	}
	if err := json.Unmarshal([]byte(out), &v); err != nil || len(v.Templates) != len(initTemplates) {
		t.Fatalf("--json: %v\n%s", err, out)
	}
}

// TestInitNextHint: tw init prints the same two next steps for every
// template (tw dev, then tw deploy --apply-policy); node-workflow runs
// npm install first and ai-agent adds a line about its mcp dependency.
func TestInitNextHint(t *testing.T) {
	if _, err := createTailworksDir(); err != nil {
		t.Skip("create-tailworks not found:", err)
	}
	for _, tmpl := range []string{"hello", "node-workflow", "ai-agent"} {
		dir := filepath.Join(t.TempDir(), "app")
		out, err := captureStdout(t, func() error { return cmdInit([]string{dir, "--template", tmpl}) })
		if err != nil {
			t.Fatal(err)
		}
		dev := "cd " + dir + " && tw dev"
		if tmpl == "node-workflow" {
			dev = "cd " + dir + " && npm install && tw dev"
		}
		for _, want := range []string{dev, "# try it here, no tailnet", "\n  tw deploy --apply-policy ", "# shows the policy lines, asks, writes, deploys"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: output lacks %q:\n%s", tmpl, want, out)
			}
		}
		if strings.Contains(out, "tw policy &&") {
			t.Errorf("%s: old hint:\n%s", tmpl, out)
		}
		if got := strings.Contains(out, "mcp template deployed"); got != (tmpl == "ai-agent") {
			t.Errorf("%s: mcp line %v:\n%s", tmpl, got, out)
		}
	}
}
