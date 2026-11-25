package ui

import (
	"strings"
	"testing"
)

// TestAuditVerificationQuietWhenOK (#406): the console leads with who did
// what. A verified trail adds no tile, badge or chain list on the
// overview and object page, and the audit page puts its integrity card
// at the bottom; a failed verification shows up everywhere, on top.
func TestAuditVerificationQuietWhenOK(t *testing.T) {
	api := populatedAPI()
	api.routes["GET /v1/audit/verification"] = map[string]any{"ok": true, "chains": []any{map[string]any{"chain": "mgmt", "ok": true, "records": 6}}, "verifiedAt": 1}
	h := serve(t, api)

	ov := get(t, h, "/ui/").Body.String()
	for _, bad := range []string{"NOT VERIFIED", `class="chains"`, `<div class="label">Audit log</div>`} {
		if strings.Contains(ov, bad) {
			t.Errorf("verified overview shows %q", bad)
		}
	}
	if !strings.Contains(ov, "Who did what") {
		t.Error("overview activity does not lead with who did what")
	}
	if ob := get(t, h, "/ui/objects/obj_root").Body.String(); strings.Contains(ob, "verified</span>") || strings.Contains(ob, `class="chains"`) {
		t.Error("object page promotes a verified chain")
	}
	ab := get(t, h, "/ui/audit").Body.String()
	integ, recs := strings.Index(ab, "all chains verified"), strings.Index(ab, "<h2>Records")
	if integ < 0 || recs < 0 || integ < recs {
		t.Errorf("audit page: integrity at %d, records at %d; want the integrity card below the records", integ, recs)
	}
	if strings.Contains(ab, "page verified") {
		t.Error("audit page badges a verified page")
	}

	// Failing: the overview tile and the audit page's top card speak up.
	h = serve(t, populatedAPI())
	if ov := get(t, h, "/ui/").Body.String(); !strings.Contains(ov, `<a class="tile alert" href="/ui/audit">`) || !strings.Contains(ov, "BROKEN at 4") {
		t.Error("overview hides a failed verification")
	}
	ab = get(t, h, "/ui/audit").Body.String()
	if bad, recs := strings.Index(ab, "NOT VERIFIED"), strings.Index(ab, "<h2>Records"); bad < 0 || bad > recs {
		t.Errorf("audit page: NOT VERIFIED at %d, records at %d; want it on top", bad, recs)
	}
}
