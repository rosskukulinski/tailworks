package ui

import (
	"regexp"
	"slices"
	"testing"

	v1 "github.com/rosskukulinski/tailworks/mgmt/api/v1"
)

// TestEnumListsMatchSpec: the objects page's status choices are the spec's
// ObjectStatus enum (in order) and the audit page's event suggestions its
// AuditKind enum (sorted), so neither can fall behind the API (issue #514).
func TestEnumListsMatchSpec(t *testing.T) {
	doc, err := v1.Spec()
	if err != nil {
		t.Fatal(err)
	}
	enum := func(name string) []string {
		var out []string
		for _, v := range doc.Components.Schemas[name].Value.Enum {
			out = append(out, v.(string))
		}
		return out
	}
	states, kinds := enum("ObjectStatus"), enum("AuditKind")
	slices.Sort(kinds)
	if len(states) < 7 || len(kinds) < 50 {
		t.Fatalf("spec enums look wrong: %d states, %d kinds", len(states), len(kinds))
	}
	h := serve(t, populatedAPI())

	body := get(t, h, "/ui/objects").Body.String()
	sel := regexp.MustCompile(`(?s)<select name="status">(.*?)</select>`).FindStringSubmatch(body)
	if sel == nil {
		t.Fatalf("no status select:\n%s", body)
	}
	var got []string
	for _, m := range regexp.MustCompile(`<option value="([^"]+)"`).FindAllStringSubmatch(sel[1], -1) {
		got = append(got, m[1])
	}
	if !slices.Equal(got, states) {
		t.Errorf("status options %v, want ObjectStatus %v", got, states)
	}

	body = get(t, h, "/ui/audit").Body.String()
	dl := regexp.MustCompile(`(?s)<datalist id="audit-kinds">(.*?)</datalist>`).FindStringSubmatch(body)
	if dl == nil {
		t.Fatalf("no audit-kinds datalist:\n%s", body)
	}
	got = nil
	for _, m := range regexp.MustCompile(`<option value="([^"]+)"`).FindAllStringSubmatch(dl[1], -1) {
		got = append(got, m[1])
	}
	if !slices.Equal(got, kinds) {
		t.Errorf("event suggestions %v, want AuditKind %v", got, kinds)
	}
}
