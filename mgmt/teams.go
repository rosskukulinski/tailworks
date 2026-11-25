package mgmt

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/rosskukulinski/tailworks/internal/tssvc"
)

// Several tenants on one tailnet, one per team (ADR-043 D4, D5,
// D7). Each tenant on a shared tailnet has:
//
//   - its own owner tag (Tenant.OwnerTag). Its OAuth client is scoped to
//     that tag, and the tag owns the tenant's workload and object tags in
//     tagOwners. Tailscale only lets a credential mint tags its own tag
//     owns, so one team's client can't mint another team's tags, even if
//     mgmt is wrong. mgmt checks ownership too, as a second layer.
//   - its own name prefix (Tenant.NamePrefix), in front of every tailnet
//     name tailworks generates for it, so two teams' "api" workloads don't
//     collide.
//
// A tenant alone on its tailnet needs neither: OwnerTag "" means tag:tw,
// as before, and NamePrefix "" means no prefix.

// DefaultOwnerTag owns a tenant's tags unless it names its own.
const DefaultOwnerTag = "tag:tw"

// maxNamePrefix keeps prefixed object names within a DNS label: an object
// ID is at most 51 characters (objectID), plus "-" and the prefix.
const maxNamePrefix = 11

// Owner is the tag that owns t's tags.
func (t Tenant) Owner() string {
	if t.OwnerTag == "" {
		return DefaultOwnerTag
	}
	return t.OwnerTag
}

// Name is a tailnet name tailworks generates for t: name with t's prefix.
func (t Tenant) Name(name string) string {
	if t.NamePrefix == "" || name == "" {
		return name
	}
	return t.NamePrefix + "-" + name
}

// twOwnTags are tailworks' infrastructure tags: never a tenant's owner tag.
var twOwnTags = []string{DefaultOwnerTag, MgmtTag, HostTag, "tag:tw-issuer", PublicTag}

func validOwnerTag(tag string) *Error {
	switch {
	case tag == "":
		return nil
	case !validTagSyntax(tag):
		return errf(400, "bad_request", "ownerTag %q is not a tag (want tag:[a-zA-Z0-9][a-zA-Z0-9._-]*)", tag)
	case slices.ContainsFunc(twOwnTags, func(o string) bool { return strings.EqualFold(o, tag) }):
		return errf(400, "reserved_tag", "ownerTag %s is one of tailworks' own tags (%s); name the team's own, e.g. tag:tw-t-payments", tag, strings.Join(twOwnTags, ", "))
	}
	return nil
}

func validNamePrefix(p string) *Error {
	if p == "" {
		return nil
	}
	if !validName(p) || len(p) > maxNamePrefix || strings.HasPrefix(p, "-") || strings.HasSuffix(p, "-") || strings.Contains(p, "--") {
		return errf(400, "bad_request", "namePrefix %q must be 1-%d of a-z, 0-9 and single inner hyphens", p, maxNamePrefix)
	}
	return nil
}

// prefixesClash: names generated under a and b could be equal ("a" and
// "a-b": a's workload "b-x" and a-b's workload "x" are both "a-b-x").
func prefixesClash(a, b string) bool {
	return a == b || strings.HasPrefix(a+"-", b+"-") || strings.HasPrefix(b+"-", a+"-")
}

// checkTeamFields validates t's owner tag and name prefix against the other
// tenants on its tailnet (create, and a PATCH of either field).
func (m *Mgmt) checkTeamFields(ctx context.Context, t Tenant) error {
	if err := validOwnerTag(t.OwnerTag); err != nil {
		return err
	}
	if err := validNamePrefix(t.NamePrefix); err != nil {
		return err
	}
	if t.Headscale() && t.OwnerTag != "" {
		return errf(400, "bad_request", "ownerTag needs Tailscale: on Headscale a tailnet keeps one tenant (ADR-043, Open)")
	}
	ts, err := m.store.ListTenants(ctx, Page{})
	if err != nil {
		return err
	}
	for _, o := range ts {
		if o.ID == t.ID || normTailnet(o.Tailnet) != normTailnet(t.Tailnet) {
			continue
		}
		if o.Owner() == DefaultOwnerTag || t.Owner() == DefaultOwnerTag || strings.EqualFold(o.Owner(), t.Owner()) {
			e := errf(409, "shared_owner_tag", "tenant %s is already on tailnet %s, and two tenants on one tailnet need different owner tags other than %s (%s owns %s's tags, %s would own this one's): "+
				"either one could mint the other's tags", o.ID, t.Tailnet, DefaultOwnerTag, o.Owner(), o.ID, t.Owner())
			e.Missing = map[string]any{"tenant": o.ID, "ownerTag": o.Owner()}
			if o.Owner() == DefaultOwnerTag {
				e.Snippet = "tw tenant set " + o.ID + " --owner-tag tag:tw-t-<team> --oauth-client-file <a client scoped to that tag>"
			}
			return e
		}
		switch {
		case o.NamePrefix == "" && t.NamePrefix == "",
			o.NamePrefix != "" && t.NamePrefix != "" && prefixesClash(o.NamePrefix, t.NamePrefix):
			e := errf(409, "name_prefix_required", "tenant %s is already on tailnet %s with name prefix %q: give this tenant a different namePrefix (and not one that starts with the other's followed by a hyphen), so their tailnet names can't collide",
				o.ID, t.Tailnet, o.NamePrefix)
			e.Missing = map[string]any{"tenant": o.ID, "namePrefix": o.NamePrefix}
			return e
		case o.NamePrefix != "" && t.NamePrefix != "":
			continue
		}
		// One of the two is unprefixed: its existing workloads must not
		// already use the other's prefix.
		bare, pre := o, t
		if t.NamePrefix == "" {
			bare, pre = t, o
		}
		ds, err := m.store.ListDeployments(ctx, DeploymentFilter{TenantID: bare.ID}, Page{})
		if err != nil {
			return err
		}
		for _, d := range ds {
			if strings.HasPrefix(d.Workload+"-", pre.NamePrefix+"-") {
				return errf(409, "name_prefix_required", "tenant %s (no name prefix) already has workload %q, which starts with %q: pick another namePrefix", bare.ID, d.Workload, pre.NamePrefix+"-")
			}
		}
	}
	return nil
}

// checkWorkloadName refuses, in a tenant without a prefix, a workload name
// that starts with another tenant's prefix on the same tailnet (its names
// would collide with that tenant's).
func (m *Mgmt) checkWorkloadName(ctx context.Context, t Tenant, workload string) error {
	if n := t.Name(workload); len(n) > 63 {
		return errf(400, "bad_request", "workload %q with tenant %s's name prefix is %q, %d characters: a tailnet name is at most 63; shorten the workload name", workload, t.ID, n, len(n))
	}
	if t.NamePrefix != "" {
		return nil
	}
	ts, err := m.store.ListTenants(ctx, Page{})
	if err != nil {
		return err
	}
	for _, o := range ts {
		if o.ID != t.ID && o.NamePrefix != "" && normTailnet(o.Tailnet) == normTailnet(t.Tailnet) && strings.HasPrefix(workload+"-", o.NamePrefix+"-") {
			return errf(400, "bad_request", "workload %q starts with %q, the name prefix of tenant %s on the same tailnet: its names would collide with that tenant's; rename it", workload, o.NamePrefix+"-", o.ID)
		}
	}
	return nil
}

// checkTenantTags is the deploy, preflight and spawn check of the tags a
// workload asks for, beyond validateObjectTag: no tenant's owner tag, and,
// for a tenant with its own owner tag and a readable policy (pol may be
// nil), each tag owned by that owner tag and by no other tenant's on the
// tailnet. Without the policy, the tenant's OAuth client is the boundary.
func (m *Mgmt) checkTenantTags(ctx context.Context, t Tenant, tags []string, pol *Policy) error {
	ts, err := m.store.ListTenants(ctx, Page{})
	if err != nil {
		return err
	}
	var others []string // other tenants' owner tags on t's tailnet
	for _, o := range ts {
		for _, tag := range tags {
			if o.OwnerTag != "" && strings.EqualFold(tag, o.OwnerTag) {
				return errf(400, "reserved_tag", "%s is tenant %s's owner tag; a workload can't ask for an owner tag", tag, o.ID)
			}
		}
		if o.ID != t.ID && normTailnet(o.Tailnet) == normTailnet(t.Tailnet) {
			others = append(others, o.Owner())
		}
	}
	if t.OwnerTag == "" || pol == nil {
		return nil
	}
	for _, tag := range tags {
		if why := tagOwnerProblem(pol, t, tag, others); why != "" {
			e := errf(400, "tag_not_owned", "%s", why)
			e.Snippet = tagOwnersPart(t, tag).Inline() // preflightsnippet.go
			e.Missing = map[string]any{"tag": tag, "owner": t.Owner()}
			return e
		}
	}
	return nil
}

// tagOwnerProblem says why tag can't be t's ("" = fine). A tag missing
// from tagOwners is left to the preflight's missingTagOwners.
func tagOwnerProblem(pol *Policy, t Tenant, tag string, others []string) string {
	owners, ok := pol.TagOwners[tag]
	if !ok {
		return ""
	}
	for _, o := range owners {
		// tag:tw-public is shared by design: every public workload node
		// carries it, and each is its own node (ADR-029).
		if tag != PublicTag && slices.Contains(others, o) {
			return fmt.Sprintf("%s is owned by %s, another tenant's owner tag on this tailnet: either tenant could mint it", tag, o)
		}
	}
	if !slices.Contains(owners, t.Owner()) {
		return fmt.Sprintf("%s is not owned by %s, tenant %s's owner tag, so its OAuth client can't mint it", tag, t.Owner(), t.ID)
	}
	return ""
}

// checkDeployTeam is Deploy's team check: the workload name against other
// tenants' prefixes, and the tags against owner tags (with the tenant's
// policy when it can be read: the OAuth client is the boundary otherwise).
func (m *Mgmt) checkDeployTeam(ctx context.Context, t Tenant, workload string, classes []ClassConfig) error {
	if err := m.checkWorkloadName(ctx, t, workload); err != nil {
		return err
	}
	var tags []string
	for _, c := range classes {
		for _, tag := range c.Tags {
			if !slices.Contains(tags, tag) {
				tags = append(tags, tag)
			}
		}
	}
	var pol *Policy
	if t.OwnerTag != "" {
		pol, _ = m.policies.Get(ctx, t)
	}
	return m.checkTenantTags(ctx, t, tags, pol)
}

// tagOwnerProblems is the preflight's list of tagOwnerProblem for tags.
func (m *Mgmt) tagOwnerProblems(ctx context.Context, t Tenant, tags []string, pol *Policy) []string {
	if t.OwnerTag == "" || pol == nil {
		return nil
	}
	var others []string
	if ts, err := m.store.ListTenants(ctx, Page{}); err == nil {
		for _, o := range ts {
			if o.ID != t.ID && normTailnet(o.Tailnet) == normTailnet(t.Tailnet) {
				others = append(others, o.Owner())
			}
		}
	}
	var out []string
	for _, tag := range tags {
		if why := tagOwnerProblem(pol, t, tag, others); why != "" {
			out = append(out, why)
		}
	}
	return out
}

// patchTeamFields applies a PATCH of ownerTag or namePrefix to t.
//
// Changing the owner tag (D7) moves a tenant to its own tag: existing
// nodes keep their tags (only who owns them changes in tagOwners), and
// the tenant needs an OAuth client scoped to the new tag, usually in the
// same PATCH (oauthClientFile). The prefix is fixed once the tenant has
// deployed: changing it would rename every node.
func (m *Mgmt) patchTeamFields(ctx context.Context, t *Tenant, p TenantPatch, changed map[string]any) error {
	if p.OwnerTag == nil && p.NamePrefix == nil {
		return nil
	}
	if p.OwnerTag != nil {
		t.OwnerTag = *p.OwnerTag
		if t.OwnerTag == DefaultOwnerTag {
			t.OwnerTag = ""
		}
		changed["ownerTag"] = t.Owner()
	}
	if p.NamePrefix != nil && *p.NamePrefix != t.NamePrefix {
		ds, err := m.store.ListDeployments(ctx, DeploymentFilter{TenantID: t.ID}, Page{Limit: 1})
		if err != nil {
			return err
		}
		if len(ds) > 0 {
			e := errf(400, "immutable_field", "namePrefix is fixed once tenant %s has deployed (changing it would rename every node); create a new tenant for a prefixed team", t.ID)
			e.Missing = map[string]any{"field": "namePrefix"}
			return e
		}
		t.NamePrefix = *p.NamePrefix
		changed["namePrefix"] = t.NamePrefix
	}
	return m.checkTeamFields(ctx, *t)
}

// applyTeamService is applyService with t's name prefix in the Service's
// name: svc:<prefix>-<name>.
func applyTeamService(t Tenant, cfg *TwConfig, workload string, classes []ClassConfig) (string, error) {
	name, err := applyService(cfg, workload, classes)
	if err != nil || name == "" || t.NamePrefix == "" {
		return name, err
	}
	name = "svc:" + t.Name(strings.TrimPrefix(name, "svc:"))
	if err := tssvc.Validate(name); err != nil {
		return "", fmt.Errorf("tailnet.service with tenant %s's name prefix %q: %v", t.ID, t.NamePrefix, err)
	}
	for i := range classes {
		if classes[i].Class == WorkloadNamespace {
			classes[i].Service = name
		}
	}
	return name, nil
}
