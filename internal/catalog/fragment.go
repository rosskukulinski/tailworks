package catalog

import (
	"fmt"
	"slices"
	"strings"
)

// Fragment kinds (changes/README.md).
const (
	KindFeature  = "feature"
	KindFix      = "fix"
	KindBreaking = "breaking"
	KindSecurity = "security"
)

// Kinds are the valid fragment kinds.
var Kinds = []string{KindFeature, KindFix, KindBreaking, KindSecurity}

// SecuritySection is the heading Markdown gives the security notes. A
// release body with it is a security release (HasSecurityNotes; the tw
// update check reads it: cmd/tw/updatecheck.go).
const SecuritySection = "Security"

// UpgradeKinds are the valid "upgrade" items of a fragment.
var UpgradeKinds = []string{
	"cli-json",
	"compat-date",
	"config-default",
	"feature-string",
	"format",
	"protocol",
	"schema-contract",
	"schema-expand",
	"workerd",
}

// ActionKinds are upgrade kinds that always put a note under "Upgrade
// actions", with or without an "Operator action:" paragraph.
var ActionKinds = []string{"schema-contract", "workerd", "config-default", "compat-date"}

// ParseFragment parses a changes/<branch>.md fragment:
//
//	---
//	kind: feature
//	upgrade: [schema-expand, protocol]
//	---
//	markdown body
//
// source is the file name recorded in the Note.
func ParseFragment(source string, b []byte) (Note, error) {
	s := strings.ReplaceAll(string(b), "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return Note{}, fmt.Errorf("%s: no front matter (the file must start with a --- line)", source)
	}
	head, body, ok := strings.Cut(s[4:], "\n---\n")
	if !ok {
		if h, found := strings.CutSuffix(s[4:], "\n---"); found {
			head, body, ok = h, "", true
		}
	}
	if !ok {
		return Note{}, fmt.Errorf("%s: front matter is not closed by a --- line", source)
	}
	n := Note{Source: source, Text: strings.TrimSpace(body)}
	seenKind, seenUpgrade := false, false
	for i, line := range strings.Split(head, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			return Note{}, fmt.Errorf("%s:%d: want key: value, got %q", source, i+2, line)
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "kind":
			if !slices.Contains(Kinds, v) {
				return Note{}, fmt.Errorf("%s: kind %q, want one of %s", source, v, strings.Join(Kinds, ", "))
			}
			n.Kind, seenKind = v, true
		case "upgrade":
			if !strings.HasPrefix(v, "[") || !strings.HasSuffix(v, "]") {
				return Note{}, fmt.Errorf("%s: upgrade must be a [list], got %q", source, v)
			}
			for _, item := range strings.Split(v[1:len(v)-1], ",") {
				item = strings.Trim(strings.TrimSpace(item), `"'`)
				if item == "" {
					continue
				}
				if !slices.Contains(UpgradeKinds, item) {
					return Note{}, fmt.Errorf("%s: upgrade kind %q, want one of %s", source, item, strings.Join(UpgradeKinds, ", "))
				}
				if !slices.Contains(n.Upgrade, item) {
					n.Upgrade = append(n.Upgrade, item)
				}
			}
			seenUpgrade = true
		default:
			return Note{}, fmt.Errorf("%s: unknown front-matter key %q (want kind, upgrade)", source, k)
		}
	}
	switch {
	case !seenKind:
		return Note{}, fmt.Errorf("%s: front matter has no kind", source)
	case !seenUpgrade:
		return Note{}, fmt.Errorf("%s: front matter has no upgrade list (use upgrade: [] for none)", source)
	case n.Text == "":
		return Note{}, fmt.Errorf("%s: the fragment has no text", source)
	}
	return n, nil
}

// IsAction reports whether n belongs under "Upgrade actions".
func (n Note) IsAction() bool {
	if strings.Contains(n.Text, "Operator action:") && !strings.Contains(n.Text, "Operator action: none") {
		return true
	}
	for _, k := range n.Upgrade {
		if slices.Contains(ActionKinds, k) {
			return true
		}
	}
	return false
}

// Markdown renders release notes for notes in sections: Breaking,
// Security, Upgrade actions, Features, Fixes. A note can appear in
// Upgrade actions and in its kind's section.
func Markdown(title string, notes []Note) string {
	var b strings.Builder
	if title != "" {
		fmt.Fprintf(&b, "# %s\n\n", title)
	}
	section := func(name string, keep func(Note) bool) {
		var sel []Note
		for _, n := range notes {
			if keep(n) {
				sel = append(sel, n)
			}
		}
		if len(sel) == 0 {
			return
		}
		fmt.Fprintf(&b, "## %s\n\n", name)
		for _, n := range sel {
			fmt.Fprintf(&b, "### %s\n\n%s\n\n", strings.TrimSuffix(n.Source, ".md"), n.Text)
		}
	}
	kind := func(k string) func(Note) bool { return func(n Note) bool { return n.Kind == k } }
	section("Breaking", kind(KindBreaking))
	section(SecuritySection, kind(KindSecurity))
	section("Upgrade actions", Note.IsAction)
	section("Features", kind(KindFeature))
	section("Fixes", kind(KindFix))
	return strings.TrimRight(b.String(), "\n") + "\n"
}

// Union is the sorted union of the notes' upgrade kinds.
func Union(notes []Note) []string {
	var out []string
	for _, n := range notes {
		for _, k := range n.Upgrade {
			if !slices.Contains(out, k) {
				out = append(out, k)
			}
		}
	}
	slices.Sort(out)
	return out
}
