package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/tailscale/hujson"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// defaultInternalClasses makes every wrangler Durable Object class that
// tailworks.jsonc's tailnet.objects leaves out internal ("node": false: a
// plain DO, as on Cloudflare, with no node of its own), issue #492. It is
// what a wrangler-only project has always got (defaultTwConfig), so adding
// a tailworks.jsonc with only the workload's tags no longer flips those
// classes to facet mode (no idFromName/newUniqueId, storage under
// Host_<Class>). Listing a class with tags opts it into a node per object
// (ADR-006).
//
// Like defaultWorkloadTags, the default is written into a.TwRaw, the
// tailworks.jsonc packed into the bundle, so hostd (which still treats an
// unlisted class as a nodeless facet, for bundles packed before this) and
// mgmt see it there.
func (a *app) defaultInternalClasses() error {
	var missing []string
	for _, c := range wranglerDOClasses(a.Wrangler) {
		if _, ok := a.Cfg.Tailnet.Objects[c]; !ok {
			missing = append(missing, c)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	raw, err := insertInternalClasses(a.TwRaw, missing)
	if err != nil {
		return fail("bad_request", "tailworks.jsonc: "+err.Error())
	}
	cfg, err := mgmt.ParseTwConfig(raw)
	if err != nil {
		return fail("bad_request", err.Error())
	}
	a.TwRaw, a.Cfg = raw, cfg
	return nil
}

// wranglerDOClasses are the classes of the wrangler config's
// durable_objects bindings that this worker defines (no script_name),
// sorted and deduplicated.
func wranglerDOClasses(w map[string]any) []string {
	dos, _ := w["durable_objects"].(map[string]any)
	bs, _ := dos["bindings"].([]any)
	var out []string
	for _, b := range bs {
		m, _ := b.(map[string]any)
		if s, _ := m["script_name"].(string); s != "" {
			continue
		}
		if c, _ := m["class_name"].(string); c != "" && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// insertInternalClasses adds "<Class>": {"node": false} to tailnet.objects
// for each class (adding "tailnet" and "objects" when missing), keeping the
// rest of the file, comments included, as it is.
func insertInternalClasses(raw []byte, classes []string) ([]byte, error) {
	v, err := hujson.Parse(raw)
	if err != nil {
		return nil, err
	}
	if _, ok := v.Value.(*hujson.Object); !ok {
		return nil, fmt.Errorf("the top level is not an object")
	}
	internal := func() map[string]any { return map[string]any{"node": false} }
	var ops []any
	switch t := v.Find("/tailnet"); {
	case t == nil:
		objs := map[string]any{}
		for _, c := range classes {
			objs[c] = internal()
		}
		ops = append(ops, map[string]any{"op": "add", "path": "/tailnet", "value": map[string]any{"objects": objs}})
	default:
		if _, ok := t.Value.(*hujson.Object); !ok {
			return nil, fmt.Errorf("tailnet is not an object")
		}
		o := t.Find("/objects")
		if o == nil {
			objs := map[string]any{}
			for _, c := range classes {
				objs[c] = internal()
			}
			ops = append(ops, map[string]any{"op": "add", "path": "/tailnet/objects", "value": objs})
			break
		}
		if _, ok := o.Value.(*hujson.Object); !ok {
			return nil, fmt.Errorf("tailnet.objects is not an object")
		}
		for _, c := range classes {
			ops = append(ops, map[string]any{"op": "add", "path": "/tailnet/objects/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(c), "value": internal()})
		}
	}
	pb, _ := json.Marshal(ops)
	if err := v.Patch(pb); err != nil {
		return nil, err
	}
	return v.Pack(), nil
}
