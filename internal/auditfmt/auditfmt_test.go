package auditfmt

import (
	"strings"
	"testing"
)

func TestDescribeApproval(t *testing.T) {
	d := map[string]any{"workload": "refunds", "workflow": "refund", "instance": "wf_1", "step": "wait-for-approval", "decision": "approve",
		"comment": "ok", "approver": map[string]any{"user": "bob@example.com", "node": "bob-laptop.a.ts.net.", "via": "tailnet"}}
	if s, ok := describeApproval("approval_decision", d); !ok || s != `refunds/refund/wf_1 step wait-for-approval approved by bob@example.com via tailnet "ok"` {
		t.Fatalf("%q", s)
	}
	if _, ok := describeApproval("kill", d); ok {
		t.Fatal("kill is not an approval")
	}
}

// TestDescribe: records read as text, never as Go format errors or raw JSON.
func TestDescribe(t *testing.T) {
	for _, c := range []struct {
		kind string
		d    map[string]any
		want string
	}{
		{"kill", map[string]any{"pushed": true}, "by - · pushed"},
		{"kill", map[string]any{"reason": "pool node idle", "requested": "o1"}, `by - "pool node idle"`},
		{"kill", map[string]any{"requested": "o0"}, "by - (cascade from o0)"},
		{"revoke", map[string]any{"keyID": "k1", "pool": "tag:a|persistent", "reason": "warm key expired unused"}, "key k1 (pool tag:a|persistent): warm key expired unused"},
		{"credential", map[string]any{"keyID": "k2", "host": "h-mini", "epoch": 1}, "key k2 for host h-mini, epoch 1"},
		{"secret_put", map[string]any{"name": "API_KEY", "workload": "w"}, "secret API_KEY of w set by -"},
	} {
		got := Describe(c.kind, "o1", "", "", c.d, nil)
		if got != c.want || strings.Contains(got, "%!") {
			t.Errorf("%s %v: got %q, want %q", c.kind, c.d, got, c.want)
		}
	}
}

func TestDecided(t *testing.T) {
	for in, want := range map[string]string{"approve": "approved", "reject": "rejected"} {
		if got := Decided(in); got != want {
			t.Errorf("Decided(%q) = %q", in, got)
		}
	}
	d := map[string]any{"workload": "w", "workflow": "f", "instance": "i", "step": "s", "decision": "reject", "approver": map[string]any{"user": "bob", "via": "ui"}}
	if got := Describe("approval_decision", "", "", "", d, nil); !strings.Contains(got, "rejected by bob") {
		t.Errorf("approval_decision: %q", got)
	}
}
