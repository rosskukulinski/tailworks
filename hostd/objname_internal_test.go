package hostd

import (
	"context"
	"testing"
)

// TestObjectNameControlsRefused (#185, ledger H13/F9 input half): a new
// object name with a C0 control, DEL, a C1 control or a bidi control is
// refused bad_object_name at admit and at ensure, and mgmt is never asked
// to create it. Names that already exist are not backfilled: they keep
// working (and render through internal/termsafe).
func TestObjectNameControlsRefused(t *testing.T) {
	t.Parallel()
	h, inst, creates := admitKillHost(t)
	as := map[string]any{"class": "Room", "name": "parent"}
	bad := []string{"a\x1b[31mb", "a\x00", "a\r\nb", "a\tb", "a\x7f", "a\u009bb", "a\u202eb", "a\u2066b"}
	for _, name := range bad {
		if code, r := platformCall(t, h, inst, "admit", map[string]any{"class": "Room", "name": name, "as": as}); code != 400 || r["code"] != "bad_object_name" {
			t.Errorf("admit Room/%q (attested): %d %v, want 400 bad_object_name", name, code, r)
		}
		if code, r := platformCall(t, h, inst, "admit", map[string]any{"class": "Open", "name": name}); code != 400 || r["code"] != "bad_object_name" {
			t.Errorf("admit Open/%q (public): %d %v, want 400 bad_object_name", name, code, r)
		}
		if code, r := platformCall(t, h, inst, "ensure", map[string]any{"as": map[string]any{"class": "Room", "name": name}}); code != 400 || r["code"] != "bad_object_name" {
			t.Errorf("ensure Room/%q: %d %v, want 400 bad_object_name", name, code, r)
		}
		h.admitLocal(inst.w, "Room", name) // even an admitted name is not created
		if _, err := h.ensure(context.Background(), inst.w, "Room", name, nil, nil, ""); asAPIError(err).Code != "bad_object_name" {
			t.Errorf("ensure Room/%q after admission: %v, want bad_object_name", name, err)
		}
	}
	if n := creates.Load(); n != 0 {
		t.Errorf("mgmt was asked to create %d object(s) with a control character in the name", n)
	}
	// No backfill: an object that already has such a name is still reached.
	liveObject(h, "obj_old", "Room", "old\x1bname")
	if code, r := platformCall(t, h, inst, "admit", map[string]any{"class": "Room", "name": "old\x1bname", "as": as}); code != 200 || r["existing"] != true {
		t.Errorf("admit of an existing name: %d %v, want 200 existing", code, r)
	}
	// Ordinary names, including non-ASCII ones, are unaffected.
	for _, name := range []string{"lobby", "room 1", "café", "部屋"} {
		if code, r := platformCall(t, h, inst, "admit", map[string]any{"class": "Room", "name": name, "as": as}); code != 200 {
			t.Errorf("admit Room/%q: %d %v", name, code, r)
		}
	}
}
