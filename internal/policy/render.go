package policy

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// One renderer for every policy snippet tailworks shows (#535). A snippet
// is built as a typed Snippet and rendered here, never assembled as a
// string by hand:
//
//   - Inline: comment-free, one line per section; the API's string fields
//     (tagOwnersSnippet, nodeAttrsSnippet, serviceSnippet, a missing
//     grant's snippet), which --json and agent consumers read.
//   - HuJSON: one entry per line; the policy-apply answer, tw up's diff.
//   - Paste: HuJSON led by MergeComment, with an optional comment after each
//     grant; every block printed for a person to paste.
//
// Grant and nodeAttrs entries keep the key order people read and the docs
// write ({ "src": …, "dst": …, "ip": …, "app": … }), with spaces.

// MergeComment leads every snippet printed for a person to paste: pasted
// as is next to an existing "tagOwners" or "grants" section, it would be a
// second key and the policy would not parse (issue #389).
const MergeComment = "// merge into your policy: put each entry inside the existing section of the same name (don't add a second one)"

// entryKeys is the order of a grant's or nodeAttrs entry's keys; others
// follow, sorted.
var entryKeys = []string{"src", "dst", "target", "ip", "attr", "app", "srcPosture", "via"}

// EntryHuJSON renders one grant or nodeAttrs entry on one line:
// { "src": ["a", "b"], "dst": ["tag:x"], "ip": ["tcp:80"] }.
func EntryHuJSON(e map[string]any) string {
	keys := make([]string, 0, len(e))
	for _, k := range entryKeys {
		if _, ok := e[k]; ok {
			keys = append(keys, k)
		}
	}
	for _, k := range slices.Sorted(maps.Keys(e)) {
		if !slices.Contains(entryKeys, k) {
			keys = append(keys, k)
		}
	}
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%q: %s", k, valueHuJSON(e[k]))
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

// valueHuJSON is a list of strings as ["a", "b"], anything else as JSON.
func valueHuJSON(v any) string {
	if l, ok := stringList(v); ok {
		return ListHuJSON(l)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// ListHuJSON is ["a", "b"].
func ListHuJSON(l []string) string {
	q := make([]string, len(l))
	for i, s := range l {
		q[i] = fmt.Sprintf("%q", s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// stringList is v as a []string when it is a list of strings.
func stringList(v any) ([]string, bool) {
	switch x := v.(type) {
	case []string:
		return x, true
	case []any:
		out := make([]string, len(x))
		for i, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			out[i] = s
		}
		return out, true
	}
	return nil, false
}

// Inline renders s comment-free with each section on one line, sections
// separated by ",\n":
//
//	"tagOwners": { "tag:a": ["autogroup:admin", "tag:tw"] },
//	"grants": [ { "src": ["tag:a"], "dst": ["tag:db"], "ip": ["tcp:5432"] } ]
func (s Snippet) Inline() string {
	var parts []string
	if len(s.TagOwners) > 0 {
		var o []string
		for _, t := range slices.Sorted(maps.Keys(s.TagOwners)) {
			o = append(o, fmt.Sprintf("%q: %s", t, ListHuJSON(s.TagOwners[t])))
		}
		parts = append(parts, `"tagOwners": { `+strings.Join(o, ", ")+` }`)
	}
	entries := func(key string, es []map[string]any) {
		if len(es) == 0 {
			return
		}
		r := make([]string, len(es))
		for i, e := range es {
			r[i] = EntryHuJSON(e)
		}
		parts = append(parts, fmt.Sprintf("%q: [ %s ]", key, strings.Join(r, ", ")))
	}
	entries("grants", s.Grants)
	entries("nodeAttrs", s.NodeAttrs)
	if len(s.AutoApprovers.Services) > 0 {
		var a []string
		for _, name := range slices.Sorted(maps.Keys(s.AutoApprovers.Services)) {
			a = append(a, fmt.Sprintf("%q: %s", name, ListHuJSON(s.AutoApprovers.Services[name])))
		}
		parts = append(parts, `"autoApprovers": { "services": { `+strings.Join(a, ", ")+` } }`)
	}
	return strings.Join(parts, ",\n")
}

// HuJSON renders the snippet for humans ("add to your policy"), one entry
// per line.
func (s Snippet) HuJSON() string { return s.render(nil) }

// Paste is HuJSON for a person to paste into their policy: led by
// MergeComment, and grantNotes[i] (when not "") as a comment after grant i.
// "" for an empty snippet.
func (s Snippet) Paste(grantNotes []string) string {
	if s.Empty() {
		return ""
	}
	return MergeComment + "\n" + strings.TrimRight(s.render(grantNotes), "\n")
}

func (s Snippet) render(grantNotes []string) string {
	var b strings.Builder
	if len(s.TagOwners) > 0 {
		b.WriteString("\"tagOwners\": {\n")
		for _, t := range slices.Sorted(maps.Keys(s.TagOwners)) {
			fmt.Fprintf(&b, "  %q: %s,\n", t, ListHuJSON(s.TagOwners[t]))
		}
		b.WriteString("},\n")
	}
	if len(s.Grants) > 0 {
		b.WriteString("\"grants\": [\n")
		for i, g := range s.Grants {
			note := ""
			if i < len(grantNotes) && grantNotes[i] != "" {
				note = " // " + grantNotes[i]
			}
			fmt.Fprintf(&b, "  %s,%s\n", EntryHuJSON(g), note)
		}
		b.WriteString("],\n")
	}
	if len(s.NodeAttrs) > 0 {
		b.WriteString("\"nodeAttrs\": [\n")
		for _, n := range s.NodeAttrs {
			fmt.Fprintf(&b, "  %s,\n", EntryHuJSON(n))
		}
		b.WriteString("],\n")
	}
	if len(s.AutoApprovers.Services) > 0 {
		b.WriteString("\"autoApprovers\": {\n  \"services\": {\n")
		for _, name := range slices.Sorted(maps.Keys(s.AutoApprovers.Services)) {
			fmt.Fprintf(&b, "    %q: %s,\n", name, ListHuJSON(s.AutoApprovers.Services[name]))
		}
		b.WriteString("  },\n},\n")
	}
	return b.String()
}
