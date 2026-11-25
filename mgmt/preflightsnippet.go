package mgmt

import (
	"slices"

	"github.com/rosskukulinski/tailworks/internal/policy"
)

// The preflight's policy snippets as typed policy.Snippet parts (#535).
// Each part is built once, here; the API's string fields
// (tagOwnersSnippet, nodeAttrsSnippet, serviceSnippet, a missing grant's
// snippet) are its policy.Snippet.Inline rendering, and PreflightResult's
// Snippet is all of them together, which tw policy --apply writes
// (preflightSnippet) and the CLI prints. Nothing is parsed back from a
// string.

// grantEntry is the grant srcs → dst on ip.
func grantEntry(srcs []string, dst, ip string) map[string]any {
	return map[string]any{"src": slices.Clone(srcs), "dst": []string{dst}, "ip": []string{ip}}
}

// missingGrant is the PreflightMissing for src → dst on ip.
func missingGrant(src, dst, ip string) PreflightMissing {
	return PreflightMissing{Src: src, Dst: dst, IP: ip, Snippet: policy.EntryHuJSON(grantEntry([]string{src}, dst, ip))}
}

// tagOwnersPart is the tagOwners entries for tags, each owned as
// tagOwnersFor(t) says (Headscale-aware).
func tagOwnersPart(t Tenant, tags ...string) policy.Snippet {
	s := policy.Snippet{TagOwners: map[string][]string{}}
	for _, tag := range tags {
		s.TagOwners[tag] = tagOwnersFor(t)
	}
	return s
}

// publicPart is the policy a public workload needs (ADR-029 D6):
// policy.PublicSnippet, with t's owner tag among tag:tw-public's owners so
// t's OAuth client can mint public nodes.
func publicPart(t Tenant) policy.Snippet {
	s := policy.PublicSnippet()
	if t.OwnerTag != "" {
		s.TagOwners[PublicTag] = append(s.TagOwners[PublicTag], t.OwnerTag)
	}
	return s
}

// servicePart is the autoApprovers.services entry that lets the workload
// node (its first tag) host the Service name.
func servicePart(name string, wlTags []string) policy.Snippet {
	tag := "tag:tw"
	if len(wlTags) > 0 {
		tag = wlTags[0]
	}
	return policy.Snippet{AutoApprovers: policy.AutoApprovers{Services: map[string][]string{name: {tag}}}}
}

// setSnippets renders what pf found missing: its Snippet and the string
// fields, from the same parts. The public part comes after the tagOwners
// part, so its tag:tw-public owners win.
func (pf *PreflightResult) setSnippets(t Tenant) {
	var all policy.Snippet
	add := func(p policy.Snippet) {
		for tag, owners := range p.TagOwners {
			if all.TagOwners == nil {
				all.TagOwners = map[string][]string{}
			}
			all.TagOwners[tag] = owners
		}
		all.Grants = append(all.Grants, p.Grants...)
		all.NodeAttrs = append(all.NodeAttrs, p.NodeAttrs...)
		if len(p.AutoApprovers.Services) > 0 {
			all.AutoApprovers = p.AutoApprovers
		}
	}
	if tags := append(slices.Clone(pf.MissingTagOwners), pf.UndefinedConnects...); len(tags) > 0 {
		p := tagOwnersPart(t, tags...)
		pf.TagOwnersSnippet = p.Inline()
		add(p)
	}
	for _, g := range pf.MissingGrants() {
		add(policy.Snippet{Grants: []map[string]any{grantEntry(g.Srcs, g.Dst, g.IP)}})
	}
	if len(pf.MissingNodeAttrs) > 0 {
		p := publicPart(t)
		pf.NodeAttrsSnippet = p.Inline()
		add(p)
	}
	if pf.ServiceUnapproved {
		p := servicePart(pf.Service, pf.workloadTags)
		pf.ServiceSnippet = p.Inline()
		add(p)
	}
	if !all.Empty() {
		pf.Snippet = &all
	}
}
