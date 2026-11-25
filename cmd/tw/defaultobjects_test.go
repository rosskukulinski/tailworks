package main

import (
	"reflect"
	"strings"
	"testing"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// TestDefaultInternalClasses: a Durable Object class missing from
// tailnet.objects is internal ("node": false, plain DO) whether or not
// tailworks.jsonc exists (issue #492); a listed class keeps its settings,
// and the deploy estimate counts the rest as sharing the workload node.
func TestDefaultInternalClasses(t *testing.T) {
	t.Parallel()
	const wr = `{"name": "shop", "main": "src/index.js", "compatibility_date": "2026-04-01",
  "durable_objects": {"bindings": [
    {"name": "ROOM", "class_name": "Room"},
    {"name": "COUNTER", "class_name": "Counter"},
    {"name": "COUNTER2", "class_name": "Counter"},
    {"name": "EXT", "class_name": "Other", "script_name": "elsewhere"}
  ]}}`
	for name, tc := range map[string]struct {
		tw        *string
		perObject []string
		shared    []string
	}{
		"no tailworks.jsonc": {shared: []string{"Counter", "Room"}},
		"empty file":         {tw: dfObjStr(`{}`), shared: []string{"Counter", "Room"}},
		"tags only":          {tw: dfObjStr("{\n  // keep me\n  \"tailnet\": {\"tags\": [\"tag:shop\"]}\n}"), shared: []string{"Counter", "Room"}},
		"one listed":         {tw: dfObjStr(`{"tailnet": {"objects": { /* keep me */ "Room": {"tags": ["tag:room"]}}}}`), perObject: []string{"Room"}, shared: []string{"Counter"}},
		"both listed":        {tw: dfObjStr(`{"tailnet": {"objects": {"Room": {"tags": ["tag:room"]}, "Counter": {"tags": ["tag:c"], "egress": "workload"}}}}`), perObject: []string{"Counter", "Room"}},
		"explicit internal":  {tw: dfObjStr(`{"tailnet": {"objects": {"Room": {"node": false}}}}`), shared: []string{"Counter", "Room"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			files := map[string]string{"wrangler.jsonc": wr}
			if tc.tw != nil {
				files["tailworks.jsonc"] = *tc.tw
			}
			writeFiles(t, dir, files)
			a, err := loadApp(dir, "")
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := mgmt.ParseTwConfig(a.TwRaw)
			if err != nil {
				t.Fatalf("%v\n%s", err, a.TwRaw)
			}
			if _, ok := cfg.Tailnet.Objects["Other"]; ok {
				t.Errorf("a script_name class was defaulted:\n%s", a.TwRaw)
			}
			for _, c := range tc.shared {
				if o, ok := cfg.Tailnet.Objects[c]; !ok || o.Node == nil || *o.Node {
					t.Errorf("%s not internal in the packed tailworks.jsonc:\n%s", c, a.TwRaw)
				}
			}
			n := estimateAppNodes(a.Cfg, false)
			if !reflect.DeepEqual(n.SharedClasses, dfObjList(tc.shared)) || !reflect.DeepEqual(n.PerObjectClasses, dfObjList(tc.perObject)) {
				t.Errorf("estimate %+v, want per-object %v, shared %v", n, tc.perObject, tc.shared)
			}
			if tc.tw != nil && strings.Contains(*tc.tw, "keep me") && !strings.Contains(string(a.TwRaw), "keep me") {
				t.Errorf("comment lost:\n%s", a.TwRaw)
			}
			if err := checkTwSchema(a.TwRaw); err != nil {
				t.Errorf("defaulted file fails the schema: %v\n%s", err, a.TwRaw)
			}
		})
	}
}

func dfObjStr(s string) *string { return &s }

func dfObjList(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
