package main

import (
	"errors"
	"maps"
	"strings"
	"testing"
)

// TestPlacementFlagsShared: deploy, promote and rollback document the
// placement flags the same way (only --allow-missing-grants names the verb).
func TestPlacementFlagsShared(t *testing.T) {
	usage := map[string]map[string]string{}
	for name, run := range map[string]func([]string) error{"deploy": cmdDeploy, "promote": cmdPromote, "rollback": cmdRollback} {
		var hr *helpRequest
		if err := run([]string{"--help"}); !errors.As(err, &hr) {
			t.Fatalf("tw %s --help: %v", name, err)
		}
		u := map[string]string{}
		for _, fl := range []string{"host", "replace", "force-takeover", "allow-missing-grants", "no-wait", "wait"} {
			l := hr.f.Lookup(fl)
			if l == nil {
				t.Fatalf("tw %s has no --%s", name, fl)
			}
			u[fl] = l.Usage
		}
		u["allow-missing-grants"] = u["allow-missing-grants"][strings.Index(u["allow-missing-grants"], " even if"):]
		usage[name] = u
	}
	if !maps.Equal(usage["deploy"], usage["promote"]) || !maps.Equal(usage["deploy"], usage["rollback"]) {
		t.Fatalf("placement flag help differs:\n%v", usage)
	}
}

func TestPlacementOptsApply(t *testing.T) {
	f := newFlags("x")
	o := addPlacementFlags(f, "deploy")
	if _, err := f.parse([]string{"--host", "h1", "--replace", "--force-takeover"}); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{}
	o.apply(body)
	if !maps.Equal(body, map[string]any{"host": "h1", "replace": true, "forceTakeover": true}) {
		t.Fatalf("body = %v", body)
	}
	f = newFlags("x")
	o = addPlacementFlags(f, "deploy")
	if _, err := f.parse(nil); err != nil {
		t.Fatal(err)
	}
	body = map[string]any{}
	o.apply(body)
	if len(body) != 0 {
		t.Fatalf("no flags: body = %v", body)
	}
}
