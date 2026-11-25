package main

import (
	"encoding/json"
	"fmt"

	"github.com/tailscale/hujson"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// defaultWorkloadTags fills in tailnet.tags when tailworks.jsonc leaves it
// out (issue #491): the workload node is tagged tag:tw-wl-<workload>, the
// tag the wrangler-only path (defaultTwConfig) has always used, so a
// minimal tailworks.jsonc is {}. The default is written into a.TwRaw, the
// tailworks.jsonc that goes into the bundle, because mgmt, hostd and
// fakemgmt read the tags from there. An explicit "tags": [] stays an
// error: mgmt reads it as "no workload node".
//
// mgmt checks a defaulted tag exactly like a written one (syntax, reserved
// tags, tagOwners), so the default grants nothing a file couldn't.
func (a *app) defaultWorkloadTags() error {
	if a.Cfg.Tailnet.Tags != nil {
		if len(a.Cfg.Tailnet.Tags) == 0 {
			return fail("bad_request", "tailworks.jsonc has an empty tailnet.tags (the workload node's tags)").
				withFix(`drop "tags" to use tag:tw-wl-` + a.Name + `, or list the tags: "tags": ["tag:…"]`)
		}
		return nil
	}
	raw, err := insertWorkloadTags(a.TwRaw, []string{"tag:tw-wl-" + a.Name})
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

// insertWorkloadTags adds "tags": tags to tailnet in a tailworks.jsonc that
// has none (adding "tailnet" too when it is missing), keeping the rest of
// the file, comments included, as it is.
func insertWorkloadTags(raw []byte, tags []string) ([]byte, error) {
	v, err := hujson.Parse(raw)
	if err != nil {
		return nil, err
	}
	if _, ok := v.Value.(*hujson.Object); !ok {
		return nil, fmt.Errorf("the top level is not an object")
	}
	var op map[string]any
	switch t := v.Find("/tailnet"); {
	case t == nil:
		op = map[string]any{"op": "add", "path": "/tailnet", "value": map[string]any{"tags": tags}}
	case t.Find("/tags") != nil:
		return raw, nil
	default:
		if _, ok := t.Value.(*hujson.Object); !ok {
			return nil, fmt.Errorf("tailnet is not an object")
		}
		op = map[string]any{"op": "add", "path": "/tailnet/tags", "value": tags}
	}
	pb, _ := json.Marshal([]any{op})
	if err := v.Patch(pb); err != nil {
		return nil, err
	}
	return v.Pack(), nil
}
