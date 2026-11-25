package main

import "time"

// placementOpts are the flags tw deploy, tw promote and tw rollback share:
// each puts a bundle on a host and waits for it.
type placementOpts struct {
	host          *string
	replace       *bool
	forceTakeover *bool
	allowMissing  *bool
	noWait        *bool
	wait          *time.Duration
}

// addPlacementFlags registers --host, --replace, --force-takeover,
// --allow-missing-grants, --no-wait and --wait with one wording for all
// three commands, and lists all but --no-wait under Advanced in --help.
// verb is the command's action ("deploy", "promote", "roll back").
func addPlacementFlags(f *flags, verb string) *placementOpts {
	o := &placementOpts{
		host:          f.String("host", "", "pin the workload to this host (it must be live, in the tenant's pool and offer the tier)"),
		replace:       f.Bool("replace", false, "re-place the workload: move it to a clearly better host (smart placement: the best measured path to tailnet.connects) instead of keeping it on its host with its Durable Object storage"),
		forceTakeover: f.Bool("force-takeover", false, "the workload's current host is gone for good: move its Durable Objects anyway (audited; see takeover_unconfirmed); with --replace, confirms moving a stateful workload off a live host"),
		allowMissing:  f.Bool("allow-missing-grants", false, verb+" even if the preflight finds policy lines the bundle needs missing (the app runs without them; on Tailscale a tag with no tagOwners entry still fails to mint)"),
		noWait:        f.Bool("no-wait", false, "return once mgmt accepted the request, without waiting for ready"),
		wait:          f.Duration("wait", 3*time.Minute, "how long to wait for ready"),
	}
	// --help lists these under Advanced (helpflags.go).
	f.group(advancedFlags,
		"host",
		"replace",
		"force-takeover",
		"allow-missing-grants",
		"wait",
	)
	return o
}

// apply puts the placement flags into a deploy, promotion or rollback
// request body.
func (o *placementOpts) apply(body map[string]any) {
	if *o.host != "" {
		body["host"] = *o.host
	}
	if *o.replace {
		body["replace"] = true
	}
	if *o.forceTakeover {
		body["forceTakeover"] = true
	}
}
