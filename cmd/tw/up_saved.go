package main

import (
	"flag"
	"strconv"
)

// keepSavedUpFlags: `tw up` again on a state dir that is already set up
// (after `tw down`, or when `tw upgrade` restarts tailworks with
// `tw up --yes`) keeps every setting it was set up with, unless that flag
// is given again. Before, each flag left out fell back to its default and
// was saved: the restart after an upgrade (and its rollback) came back with
// another host ID, hostname prefix, control or API URL, --ephemeral,
// --allow-sibling-egress, warm keys, … and, on a --local-dev state, failed
// with state_conflict (issue #111).
//
// --workerd is not kept: the pinned workerd comes from the install (or
// $TW_WORKERD), so an upgrade picks up the new release's. The host timing
// and push port are no longer flags (up_tuning.go): tw.json's values are
// used as saved.
func keepSavedUpFlags(fs *flag.FlagSet, c Config) {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	keep := func(name, saved string) {
		if !given[name] && saved != "" && fs.Lookup(name) != nil {
			fs.Set(name, saved)
		}
	}
	keep("control-url", c.ControlURL)
	keep("tsapi-url", c.TSAPIURL)
	keep("host-id", c.HostID)
	keep("hostname-prefix", c.HostnamePrefix)
	keep("ephemeral", strconv.FormatBool(c.Ephemeral))
	keep("insecure-shared-host", strconv.FormatBool(c.InsecureSharedHost))
	keep("allow-experimental-flags", strconv.FormatBool(c.AllowExperimentalFlags))
	keep("allow-sibling-egress", strconv.FormatBool(c.AllowSiblingEgress))
	keep("local-dev", strconv.FormatBool(c.LocalDev))
	keep("port-mapping", strconv.FormatBool(c.PortMapping))
	keep("db", c.DB)
	keep("db-file", c.DBFile)
	keep("custody", c.Custody)
	keep("setec-url", c.SetecURL)
	keepSavedRetention(keep, c) // up_retention.go
	keep("worker-internet", c.WorkerInternet)
	if c.WarmKeys != nil {
		keep("warm-keys", strconv.Itoa(*c.WarmKeys))
	}
	if c.PoolNodes != nil {
		keep("pool-nodes", strconv.Itoa(*c.PoolNodes))
	}
}
