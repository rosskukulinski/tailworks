package main_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/e2e/agenteval"
	"github.com/rosskukulinski/tailworks/e2e/cliflow"
)

// checkStatus runs tw status on e: its --json must match $defs/status (on
// the host and on a linked machine alike, #455) and the human output must
// lead with the verdict and contain each of want.
func checkStatus(t *testing.T, e *cliflow.Env, want ...string) map[string]any {
	t.Helper()
	so, se, _, err := e.Tw("status", "--json")
	if err != nil {
		t.Fatalf("tw status --json: %v\n%s\n%s", err, so, se)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(so), &v); err != nil {
		t.Fatalf("tw status --json: %v\n%s", err, so)
	}
	if err := agenteval.CheckDoc("status", v); err != nil {
		t.Fatal(err)
	}
	txt, se, _, err := e.Tw("status")
	if err != nil || !strings.HasPrefix(txt, "tw status · healthy · ") {
		t.Fatalf("tw status should lead with the verdict: %v\n%s\n%s", err, txt, se)
	}
	for _, w := range want {
		if !strings.Contains(txt, w) {
			t.Errorf("tw status: no %q in\n%s", w, txt)
		}
	}
	return v
}
