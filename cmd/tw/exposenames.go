package main

// tw expose names | tw expose release <name> (issue #521): the CLI side of
// public-name ownership (ADR-029, red-team H10; mgmt/publicnames.go). A
// tenant keeps a public name across undeploys until it releases it.

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/rosskukulinski/tailworks/mgmt"
)

// exposeNamesResult is tw expose names --json.
type exposeNamesResult struct {
	TenantID string                 `json:"tenantID"`
	Names    []mgmt.PublicNameClaim `json:"names"`
}

// exposeReleaseResult is tw expose release --json.
type exposeReleaseResult struct {
	TenantID string `json:"tenantID"`
	Name     string `json:"name"`
	Released bool   `json:"released"`
}

// cmdExposeNames runs `tw expose names` and `tw expose release <name>`.
func cmdExposeNames(f *flags, tenant string, pos []string) error {
	switch {
	case pos[0] == "names" && len(pos) == 1:
	case pos[0] == "release" && len(pos) == 2:
	default:
		return fail("bad_request", "tw expose names takes no argument; tw expose release takes the public name", "usage: tw "+usageOf("expose"))
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if err := s.requireUp(); err != nil {
		return err
	}
	tn, err := s.tenant(tenant)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	base := "/v1/tenants/" + tn.TenantID + "/public-names"
	if pos[0] == "names" {
		names, err := listAll[mgmt.PublicNameClaim](ctx, s.mgmt(), base, 0)
		if err != nil {
			return err
		}
		res := exposeNamesResult{TenantID: tn.TenantID, Names: names}
		if res.Names == nil {
			res.Names = []mgmt.PublicNameClaim{}
		}
		emit(f, res, func() {
			if len(names) == 0 {
				fmt.Printf("tenant %s owns no public names\n", tn.Tailnet)
				return
			}
			fmt.Printf("%-32s %-20s %s\n", "NAME", "CLAIMED", "BY")
			for _, n := range names {
				by := n.ClaimedBy
				if by == "" {
					by = "-"
				}
				fmt.Printf("%-32s %-20s %s\n", n.Name, time.UnixMilli(n.ClaimedAt).Local().Format("2006-01-02 15:04"), by)
			}
			fmt.Printf("  a name stays this tenant's across undeploys · free one: tw expose release <name>\n")
		})
		return nil
	}
	name := pos[1]
	err = s.mgmt().DoHeaders(ctx, "DELETE", base+"/"+url.PathEscape(name), nil, nil, map[string]string{"Tw-Actor": actor()})
	if err != nil {
		ce := asCLIErr(err)
		if ce.Code == "not_found" {
			ce.Fix = append(ce.Fix, "tw expose names   (the public names this tenant owns)")
		}
		return ce
	}
	emit(f, exposeReleaseResult{TenantID: tn.TenantID, Name: name, Released: true}, func() {
		fmt.Printf("✓ released   public name %s (tenant %s)\n", name, tn.Tailnet)
		fmt.Printf("  any tenant on the tailnet can now deploy public routes under it; senders still using its URL reach whoever does\n")
	})
	return nil
}
