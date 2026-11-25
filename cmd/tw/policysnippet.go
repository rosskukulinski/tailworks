package main

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/policy"
	"github.com/rosskukulinski/tailworks/mgmt"
	"github.com/tailscale/hujson"
)

// The policy blocks tw prints for a person to paste (#535): each is a
// typed policy.Snippet rendered by Snippet.Paste, so every block has the
// same shape and the same lead comment (merge each entry into the existing
// section; issue #389).

// preflightPolicy is what a deploy preflight found missing, as one typed
// snippet (mgmt's PreflightResult.Snippet). The caller grants are never in
// it: they are mgmt's to write, and printed on lines of their own.
func preflightPolicy(pf mgmt.PreflightResult) policy.Snippet {
	if pf.Snippet != nil {
		return *pf.Snippet
	}
	return legacyPreflightPolicy(pf)
}

// legacyPreflightPolicy reads a preflight from a mgmt older than
// PreflightResult.Snippet (the CLI may be newer than mgmt,
// docs/upgrading.md) from its string fields.
func legacyPreflightPolicy(pf mgmt.PreflightResult) policy.Snippet {
	var s policy.Snippet
	add := func(part string) {
		var p policy.Snippet
		std, err := hujson.Standardize([]byte("{" + part + "}"))
		if part == "" || err != nil || json.Unmarshal(std, &p) != nil {
			return
		}
		for t, o := range p.TagOwners {
			if s.TagOwners == nil {
				s.TagOwners = map[string][]string{}
			}
			s.TagOwners[t] = o
		}
		s.NodeAttrs = append(s.NodeAttrs, p.NodeAttrs...)
		if len(p.AutoApprovers.Services) > 0 {
			s.AutoApprovers = p.AutoApprovers
		}
	}
	if len(pf.MissingTagOwners) > 0 || len(pf.UndefinedConnects) > 0 {
		add(pf.TagOwnersSnippet)
	}
	for _, g := range pf.MissingGrants() {
		s.Grants = append(s.Grants, map[string]any{"src": g.Srcs, "dst": []string{g.Dst}, "ip": []string{g.IP}})
	}
	if len(pf.MissingNodeAttrs) > 0 {
		add(pf.NodeAttrsSnippet)
	}
	if pf.ServiceUnapproved {
		add(pf.ServiceSnippet)
	}
	return s
}

// snippetConnects is s's tagOwners and grants: what the app's own tags and
// tailnet.connects need. tag:tw-public's owners go with the public part.
func snippetConnects(s policy.Snippet) policy.Snippet {
	out := policy.Snippet{Grants: s.Grants}
	for t, o := range s.TagOwners {
		if t == policy.PublicTag && len(s.NodeAttrs) > 0 {
			continue
		}
		if out.TagOwners == nil {
			out.TagOwners = map[string][]string{}
		}
		out.TagOwners[t] = o
	}
	return out
}

// snippetPublic is s's public-ingress part (ADR-029): the nodeAttrs and
// tag:tw-public's owners.
func snippetPublic(s policy.Snippet) policy.Snippet {
	if len(s.NodeAttrs) == 0 {
		return policy.Snippet{}
	}
	out := policy.Snippet{NodeAttrs: s.NodeAttrs}
	if o, ok := s.TagOwners[policy.PublicTag]; ok {
		out.TagOwners = map[string][]string{policy.PublicTag: o}
	}
	return out
}

// snippetService is s's autoApprovers.services part.
func snippetService(s policy.Snippet) policy.Snippet {
	return policy.Snippet{AutoApprovers: s.AutoApprovers}
}

// pasteLines is s.Paste(notes) as lines, each prefixed with indent (nil
// for an empty snippet).
func pasteLines(s policy.Snippet, notes []string, indent string) []string {
	p := s.Paste(notes)
	if p == "" {
		return nil
	}
	lines := strings.Split(p, "\n")
	for i, l := range lines {
		lines[i] = indent + l
	}
	return lines
}

// printPaste prints pasteLines.
func printPaste(s policy.Snippet, indent string) {
	for _, l := range pasteLines(s, nil, indent) {
		fmt.Println(l)
	}
}

// entryStrs is a grant's list field k ("dst") as strings.
func entryStrs(g map[string]any, k string) []string {
	switch v := g[k].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
