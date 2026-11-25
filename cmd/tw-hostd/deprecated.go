package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
)

// Deprecated tw-hostd flags. They stay registered for one release, because
// -self-upgrade hosts restart with their service unit's argv, but are left
// out of -help. Remove them in the release after.

// hostdHiddenFlags are registered but not listed by -help.
var hostdHiddenFlags = map[string]bool{
	"default-tier": true,
}

// hostdDeprecatedFlags registers the deprecated flags on fs and makes its
// usage skip them. It returns -default-tier's value.
func hostdDeprecatedFlags(fs *flag.FlagSet) *string {
	tier := fs.String("default-tier", "", "deprecated and ignored: set the tier per tenant (tw tenant set <tenant> --tier microvm) or per deployment (\"tier\" in tailworks.jsonc)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage of %s:\n", os.Args[0])
		shown := flag.NewFlagSet(fs.Name(), flag.ContinueOnError)
		shown.SetOutput(fs.Output())
		fs.VisitAll(func(f *flag.Flag) {
			if !hostdHiddenFlags[f.Name] {
				shown.Var(f.Value, f.Name, f.Usage)
			}
		})
		shown.PrintDefaults()
	}
	return tier
}

// hostdCheckDefaultTier handles the removed -default-tier. "process" was
// already the default, so it is ignored with a warning. "microvm" is refused:
// ignoring it would quietly run unlabelled deployments in the process tier,
// and it was never a per-host floor anyway (a deployment's or tenant's tier
// overrode it, and mgmt placement never saw it).
func hostdCheckDefaultTier(tier string, log *slog.Logger) error {
	switch tier {
	case "":
		return nil
	case "process":
		log.Warn("-default-tier is deprecated and ignored (process is the default); remove it from this host's command line")
		return nil
	default:
		return fmt.Errorf("-default-tier %s: the flag was removed; set the tier per tenant (tw tenant set <tenant> --tier microvm) or per deployment (\"tier\": \"microvm\" in tailworks.jsonc), then drop -default-tier from this host's command line. The host default was never an isolation floor: a deployment's or tenant's tier overrode it, and mgmt placement could not see it", tier)
	}
}
