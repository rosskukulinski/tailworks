package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/tailscale/hujson"

	"github.com/rosskukulinski/tailworks/mgmt"
)

var camelRE = regexp.MustCompile(`([a-z0-9])([A-Z])`)

// classLabel turns a DO class name into a tag fragment: SupportAgent ->
// support-agent.
func classLabel(class string) string {
	return workloadLabel(camelRE.ReplaceAllString(class, "${1}-${2}"))
}

// appTags renames a template's tags after the new app, so two apps made
// from one template don't share tags (and with them every grant):
// tailnet.tags become tag:tw-wl-<workload>, and each class's tags
// tag:tw-<workload>-<class>. Only tailworks' own template tags ("tag:tw-…")
// are renamed; it returns the new tailworks.jsonc and the old -> new map.
func appTags(raw []byte, wl string) ([]byte, map[string]string, error) {
	cfg, err := mgmt.ParseTwConfig(raw)
	if err != nil {
		return nil, nil, err
	}
	rename := map[string]string{}
	add := func(old, nu string) {
		if strings.HasPrefix(old, "tag:tw-") && old != nu {
			rename[old] = nu
		}
	}
	for _, t := range cfg.Tailnet.Tags {
		add(t, "tag:tw-wl-"+wl)
	}
	classes := make([]string, 0, len(cfg.Tailnet.Objects))
	for c := range cfg.Tailnet.Objects {
		classes = append(classes, c)
	}
	slices.Sort(classes)
	for _, c := range classes {
		for _, t := range cfg.Tailnet.Objects[c].Tags {
			if _, done := rename[t]; !done {
				add(t, "tag:tw-"+wl+"-"+classLabel(c))
			}
		}
	}
	// One pass over the quoted template tags, each looked up in rename. A
	// pass per renamed tag (in map order) could rename a tag twice when one
	// rename's result is another's input: with a workload named "a" and a
	// class B, tag:tw-wl-b becomes tag:tw-wl-a, which must not then become
	// class B's tag:tw-a-b.
	out := quotedTwTagRE.ReplaceAllFunc(raw, func(q []byte) []byte {
		if nu, ok := rename[string(q[1:len(q)-1])]; ok {
			return []byte(`"` + nu + `"`)
		}
		return q
	})
	return out, rename, nil
}

var quotedTwTagRE = regexp.MustCompile(`"tag:tw-[^"]*"`)

// initTags lists the tags of the app in dir, for tw init's summary.
func initTags(dir string) []string {
	a, err := loadApp(dir, "")
	if err != nil {
		return nil
	}
	return a.srcTags()
}

// initTagOverrides are tw init's --tags and --object-tags: tags that
// already exist in the tailnet policy, used instead of new per-app ones so
// a new app needs no policy change.
type initTagOverrides struct {
	workload   []string            // --tags: tailnet.tags
	allClasses []string            // --object-tags tag:a,…: every class with its own node
	classes    map[string][]string // --object-tags Class=tag:a,…: one class
}

func (o initTagOverrides) empty() bool {
	return len(o.workload) == 0 && len(o.allClasses) == 0 && len(o.classes) == 0
}

// parseTagList parses "tag:a,tag:b" for flag, checking each tag the way
// deploy will.
func parseTagList(flag, s string) ([]string, error) {
	var out []string
	for _, t := range strings.Split(s, ",") {
		t = strings.TrimSpace(t)
		switch {
		case t == "":
			continue
		case !mgmt.ValidTagSyntax(t):
			return nil, fail("bad_request", fmt.Sprintf("--%s: %q is not a valid tag (want tag:[a-zA-Z0-9][a-zA-Z0-9._-]*)", flag, t))
		case mgmt.IsReservedTag(t):
			return nil, fail("reserved_tag", fmt.Sprintf("--%s: %s is one of tailworks' own tags and can't be given to a workload", flag, t))
		}
		if !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil, fail("bad_request", "--"+flag+" needs at least one tag, e.g. tag:tw-app")
	}
	return out, nil
}

func parseInitTagOverrides(tags string, objectTags []string) (initTagOverrides, error) {
	var o initTagOverrides
	if tags != "" {
		l, err := parseTagList("tags", tags)
		if err != nil {
			return o, err
		}
		o.workload = l
	}
	for _, v := range objectTags {
		class, list, perClass := strings.Cut(v, "=")
		if !perClass {
			if o.allClasses != nil {
				return o, fail("bad_request", "--object-tags without a class is given twice").withFix("tw init … --object-tags tag:a,tag:b, or --object-tags <Class>=tag:a per class")
			}
			l, err := parseTagList("object-tags", v)
			if err != nil {
				return o, err
			}
			o.allClasses = l
			continue
		}
		class = strings.TrimSpace(class)
		if class == "" {
			return o, fail("bad_request", fmt.Sprintf("--object-tags %q: no class before =", v))
		}
		l, err := parseTagList("object-tags", list)
		if err != nil {
			return o, err
		}
		if o.classes == nil {
			o.classes = map[string][]string{}
		}
		if _, dup := o.classes[class]; dup {
			return o, fail("bad_request", "--object-tags: class "+class+" is given twice")
		}
		o.classes[class] = l
	}
	return o, nil
}

var tagsArrayRE = regexp.MustCompile(`"tags"(\s*:\s*)\[([^\]]*)\]`)

// setTags rewrites the tag arrays in a tailworks.jsonc to o, keeping the
// rest of the file (comments included) as it is. It finds each array by its
// current tags, so it refuses when two nodes share a tag set and only one
// of them would change.
func setTags(raw []byte, o initTagOverrides) ([]byte, error) {
	cfg, err := mgmt.ParseTwConfig(raw)
	if err != nil {
		return nil, err
	}
	if cfg.Tailnet.Tags == nil && len(o.workload) > 0 {
		// No tailnet.tags to rewrite (it defaults to tag:tw-wl-<workload>,
		// defaulttags.go): add the line.
		if raw, err = insertWorkloadTags(raw, o.workload); err != nil {
			return nil, err
		}
		o.workload = nil
	}
	type owner struct {
		who      string
		cur, new []string
	}
	owners := []owner{{who: "the workload (tailnet.tags)", cur: cfg.Tailnet.Tags, new: o.workload}}
	var classes, nodeClasses []string
	for c := range cfg.Tailnet.Objects {
		classes = append(classes, c)
	}
	slices.Sort(classes)
	for _, c := range classes {
		if len(cfg.Tailnet.Objects[c].Tags) > 0 {
			nodeClasses = append(nodeClasses, c)
		}
	}
	if len(o.allClasses) > 0 && len(nodeClasses) == 0 {
		return nil, fail("bad_request", "--object-tags: this template has no Durable Object class with a node of its own (tailnet.objects with tags)")
	}
	for c := range o.classes {
		oc, ok := cfg.Tailnet.Objects[c]
		switch {
		case !ok:
			return nil, fail("bad_request", "--object-tags: the template has no class "+c, "classes with a node of their own: "+strings.Join(nodeClasses, ", "))
		case len(oc.Tags) == 0:
			return nil, fail("bad_request", "--object-tags: class "+c+" has no node of its own (no tags in the template), so it has no tags to set")
		}
	}
	for _, c := range nodeClasses {
		nu := o.classes[c]
		if nu == nil {
			nu = o.allClasses
		}
		owners = append(owners, owner{who: "class " + c, cur: cfg.Tailnet.Objects[c].Tags, new: nu})
	}
	want := map[string][]string{}
	for _, a := range owners {
		k := strings.Join(a.cur, ",")
		for _, b := range owners {
			if strings.Join(b.cur, ",") == k && !slices.Equal(a.new, b.new) {
				return nil, fail("bad_request", fmt.Sprintf("%s and %s have the same tags in the template (%s), so tw init can't set one without the other", a.who, b.who, k)).withFix("give both the same tags, or edit tailnet.tags / tailnet.objects in tailworks.jsonc by hand")
			}
		}
		if a.new != nil {
			want[k] = a.new
		}
	}
	found := map[string]bool{}
	raw = tagsArrayRE.ReplaceAllFunc(raw, func(m []byte) []byte {
		sub := tagsArrayRE.FindSubmatch(m)
		std, err := hujson.Standardize(append([]byte("["), append(append([]byte(nil), sub[2]...), ']')...))
		var cur []string
		if err != nil || json.Unmarshal(std, &cur) != nil {
			return m
		}
		k := strings.Join(cur, ",")
		nu, ok := want[k]
		if !ok {
			return m
		}
		found[k] = true
		q := make([]string, len(nu))
		for i, t := range nu {
			q[i] = `"` + t + `"`
		}
		return []byte(`"tags"` + string(sub[1]) + "[" + strings.Join(q, ", ") + "]")
	})
	for k := range want {
		if !found[k] {
			return nil, fail("bad_request", "tw init couldn't find the tags ["+k+"] in tailworks.jsonc to replace").withFix("edit tailnet.tags / tailnet.objects in tailworks.jsonc by hand")
		}
	}
	return raw, nil
}
