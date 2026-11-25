package main

import (
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	twversion "github.com/rosskukulinski/tailworks/internal/version"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// tw upgrade's subcommands (issue #486): `tw upgrade` upgrades this
// machine; `tw upgrade fleet` plans and runs a fleet upgrade through
// tw-mgmt (status, pause, resume, abort below it); import, releases and
// rollback are in their own files. The old spellings (--fleet, a
// top-level status|pause|resume|abort, tw release) still work for one
// release, hidden, with a note on stderr.

// upgradeRunActions are what `tw upgrade fleet <action> [<run-id>]` does
// to an upgrade run.
var upgradeRunActions = []string{
	"abort",
	"pause",
	"resume",
	"status",
}

// upgradeValueFlags are the flags of tw upgrade and its subcommands that
// take a value, so upgradeSubcommand doesn't mistake a value for the
// subcommand.
var upgradeValueFlags = []string{
	"canary",
	"from",
	"health-timeout",
	"hosts",
	"platform",
	"prefix",
	"settle",
	"soak",
	"state-dir",
}

// upgradeSubcommand finds the first positional argument in args. When it
// is one of subs, it returns it and args without it; otherwise "" and
// args unchanged.
func upgradeSubcommand(args []string, subs ...string) (string, []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		if strings.HasPrefix(a, "-") {
			name := strings.TrimLeft(a, "-")
			if !strings.Contains(name, "=") && slices.Contains(upgradeValueFlags, name) {
				i++
			}
			continue
		}
		if slices.Contains(subs, a) {
			return a, append(append([]string{}, args[:i]...), args[i+1:]...)
		}
		break
	}
	return "", args
}

// upgradeDeprecated notes on stderr that old is now spelled now (not with
// --json: stderr is the error object's there).
func upgradeDeprecated(args []string, old, now string) {
	if wantsJSON(args) {
		return
	}
	fmt.Fprintf(os.Stderr, "note: %s is now %s; the old spelling goes away in the next release\n", old, now)
}

// upgradeFleetOpts are tw upgrade fleet's flags.
type upgradeFleetOpts struct {
	hosts, canary *string
	soak, settle  *time.Duration
	noWait        *bool
	planOnly, yes *bool
	names         []string // the fleet-only ones (not --plan, --yes)
}

// upgradeFleetFlags registers the fleet-only flags on f; planOnly and yes
// are f's --plan and --yes when it already has them.
func upgradeFleetFlags(f *flags, planOnly, yes *bool) *upgradeFleetOpts {
	o := &upgradeFleetOpts{planOnly: planOnly, yes: yes}
	if o.planOnly == nil {
		o.planOnly = f.Bool("plan", false, "print the plan and start nothing")
		o.yes = yesFlagBefore(f, "starting the upgrade run")
	}
	o.hosts = f.String("hosts", "", "only these hosts (comma-separated)")
	o.canary = f.String("canary", "", "the canary host (default: the first)")
	o.soak = f.Duration("soak", mgmt.DefaultUpgradeSoak, "watch the canary this long before the other hosts")
	o.settle = f.Duration("settle", mgmt.DefaultUpgradeSettle, "how long a host may take to come back and settle")
	o.noWait = f.Bool("no-wait", false, "start the run and return (tw upgrade fleet status follows it)")
	o.names = []string{"canary", "hosts", "no-wait", "settle", "soak"}
	return o
}

func (o *upgradeFleetOpts) request(want string) mgmt.UpgradeRequest {
	req := mgmt.UpgradeRequest{To: want, Hosts: splitComma(*o.hosts), Canary: *o.canary}
	so, se := int(o.soak.Seconds()), int(o.settle.Seconds())
	req.SoakSeconds, req.SettleSeconds = &so, &se
	return req
}

// setFlags lists which of names were given on the command line.
func setFlags(f *flags, names []string) []string {
	var out []string
	f.Visit(func(fl *flag.Flag) {
		if slices.Contains(names, fl.Name) {
			out = append(out, "--"+fl.Name)
		}
	})
	return out
}

func cmdUpgradeFleet(args []string) error {
	if sub, rest := upgradeSubcommand(args, upgradeRunActions...); sub != "" {
		return cmdUpgradeRun(sub, rest)
	}
	f := newFlags("upgrade fleet")
	o := upgradeFleetFlags(f, nil, nil)
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	want, err := upgradeWantVersion(pos, "tw upgrade fleet")
	if err != nil {
		return err
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	return fleetUpgrade(f, s, o.request(want), *o.planOnly, *o.yes, *o.noWait) // upgrade.go
}

// upgradeWantVersion is the optional <version> argument.
func upgradeWantVersion(pos []string, cmd string) (string, error) {
	if len(pos) > 1 {
		return "", fail("bad_request", cmd+" takes at most one version", "usage: tw "+upgradeUsage)
	}
	if len(pos) == 0 {
		return "", nil
	}
	v, err := twversion.Parse(pos[0])
	if err != nil {
		return "", fail("bad_request", err.Error(), "usage: tw "+upgradeUsage)
	}
	return v.String(), nil
}
