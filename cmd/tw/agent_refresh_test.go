package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	twagent "github.com/rosskukulinski/tailworks/agent"
)

// tw agent setup --refresh (what tw upgrade runs with the new binary,
// issue #490) updates only what tw wrote, with no agent detected and no
// question asked; with nothing tw wrote it is a no-op, and tw upgrade's
// plan only lists the step when there is something to refresh.
func TestAgentSetupRefresh(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	if st := agentRefreshStep(); st != "" {
		t.Fatalf("plan step with nothing to refresh: %q", st)
	}
	out, err := captureStdout(t, func() error {
		return cmdAgent([]string{"setup", "--scope", "user", "--yes", "--refresh", "--json"})
	})
	if err != nil || strings.Contains(out, `"create"`) {
		t.Fatalf("empty home: %v %s", err, out)
	}

	core := filepath.Join(home, ".claude", "skills", twagent.CoreSkill, "SKILL.md")
	os.MkdirAll(filepath.Dir(core), 0o755)
	os.WriteFile(core, []byte("<!-- tw agent skills 0.0.1 -->\nold\n"), 0o644)
	if agentRefreshStep() == "" {
		t.Fatal("no plan step with a stamped skill")
	}
	out, err = captureStdout(t, func() error {
		return cmdAgent([]string{"setup", "--scope", "user", "--yes", "--refresh", "--json"})
	})
	var res struct {
		Changes []twagent.Change `json:"changes"`
		Applied bool             `json:"applied"`
	}
	if err != nil || json.Unmarshal([]byte(out), &res) != nil || !res.Applied {
		t.Fatalf("refresh: %v %s", err, out)
	}
	for _, c := range res.Changes {
		if c.Action == twagent.Create || c.Action == twagent.Skip {
			t.Errorf("refresh planned %s %s", c.Action, c.Path)
		}
	}
	if b, _ := os.ReadFile(core); strings.Contains(string(b), "0.0.1") {
		t.Fatalf("core skill not refreshed: %q", b)
	}
	if err := cmdAgent([]string{"remove", "--refresh", "--dry-run"}); err == nil {
		t.Fatal("tw agent remove accepted --refresh")
	}
}
