package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/hostd"
	"github.com/rosskukulinski/tailworks/internal/hostdrun"
)

// `tw up --worker-internet=direct|exit-node=<node>|off` (issue #295, item
// 5; ADR-049; ADR-054): the host-wide switch for Workers'
// internet egress, tw-hostd's -worker-internet. Saved in tw.json
// (Config.WorkerInternet) so a restart and the `tw up --yes` that `tw
// upgrade` runs keep it; passed to hostd in both layouts; shown by `tw
// status` and `tw doctor`. Each workload's tailnet.egress.internet still
// narrows "direct" and "exit-node"; "off" refuses every internet fetch on
// this host (internet_egress_unavailable). With exit-node, containers'
// internet path is the same exit node (a host has one internet path).

const (
	workerInternetDirect   = hostdrun.InternetDirect
	workerInternetOff      = hostdrun.InternetOff
	workerInternetExitNode = hostdrun.InternetExitNode
)

func addWorkerInternetFlag(f *flags) *string {
	f.group(upGroupIsolation, "worker-internet")
	return f.String("worker-internet", workerInternetDirect, "how Workers' fetch() reaches internet hosts, subject to each workload's tailnet.egress.internet: "+hostdrun.InternetModeHelp)
}

// saveWorkerInternet validates the flag and records it in tw.json.
func saveWorkerInternet(c *Config, v string) error {
	kind, spec, err := hostdrun.ParseInternetMode(v)
	if err != nil {
		return fail("bad_request", "--worker-internet "+err.Error())
	}
	if kind == workerInternetExitNode && spec == "" {
		return fail("bad_request", fmt.Sprintf("--worker-internet %q: exit-node needs a node: want %s", v, hostdrun.InternetModeHelp))
	}
	c.WorkerInternet = kind
	if spec != "" {
		c.WorkerInternet += "=" + spec
	}
	return nil
}

// workerInternet is the saved setting; state written before it existed
// (empty) is "direct", hostd's default.
func (c Config) workerInternet() string {
	if c.WorkerInternet == "" {
		return workerInternetDirect
	}
	return c.WorkerInternet
}

// workerInternetKind is the saved setting's kind (direct, off, exit-node).
func (c Config) workerInternetKind() string {
	kind, _, _ := hostdrun.ParseInternetMode(c.workerInternet())
	return kind
}

// workerInternetHostdArgs are tw-hostd's flags for it (--separate-host);
// only "direct" is tw-hostd's default. An exit node is the containers'
// path too.
func (c Config) workerInternetHostdArgs() []string {
	switch c.workerInternetKind() {
	case workerInternetDirect:
		return nil
	case workerInternetExitNode:
		return []string{"-worker-internet", c.workerInternet(), "-container-internet", workerInternetExitNode}
	}
	return []string{"-worker-internet", c.workerInternet()}
}

// workerInternetSingle sets it on the embedded host (single process).
func (c Config) workerInternetSingle(ho *hostdrun.Options) {
	ho.WorkerInternet = c.workerInternet()
	if c.workerInternetKind() == workerInternetExitNode {
		ho.ContainerInternet = workerInternetExitNode
	}
}

// workerInternetDetail describes the setting for `tw status` and
// `tw doctor`. running is what the host reports ("" when it doesn't say:
// not running, or an older host); problem is why the host's exit node is
// unusable right now (""); a host still running another setting than
// tw.json's is called out.
func workerInternetDetail(saved, running, problem string) string {
	v := running
	if v == "" {
		v = saved
	}
	kind, spec, _ := hostdrun.ParseInternetMode(v)
	var d string
	switch kind {
	case workerInternetOff:
		d = v + " (Workers' fetch() to internet hosts is refused on this host)"
	case workerInternetExitNode:
		node := spec
		if sp, err := hostd.ParseExitNodeSpec(spec); err == nil {
			node = sp.Node
		}
		d = v + " (Workers' and containers' internet traffic leaves through tailnet exit node " + node + " via this host's own node, narrowed by each workload's tailnet.egress.internet)"
		if problem != "" {
			d += "; unavailable: " + problem
		}
	default:
		d = v + " (Workers' fetch() reaches public internet hosts through this host's network, narrowed by each workload's tailnet.egress.internet)"
	}
	if running != "" && running != saved {
		d += "; tw.json says " + saved + ": restart tailworks (tw down, then tw up) to apply it"
	}
	return d
}

// printWorkerInternet is tw status's line for it.
func printWorkerInternet(saved, running, problem string) {
	mark := "✓"
	if running != "" && running != saved || problem != "" {
		mark = "!"
	}
	fmt.Printf("%s internet    Workers %s\n", mark, workerInternetDetail(saved, running, problem))
}

// hostWorkerInternet is what the running host reports (StatusView): the
// path, and why its exit node is unusable ("" when it is, or no exit node).
func hostWorkerInternet(s *state) (running, problem string) {
	if !s.supervisorAlive() {
		return "", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var hv hostd.StatusView
	if s.hostd().Do(ctx, "GET", "/v1/status", nil, &hv) != nil {
		return "", ""
	}
	return hv.WorkerInternet, hv.WorkerInternetProblem
}

// exitNodeFix is tw doctor's fix for an unusable exit node.
const exitNodeFix = "on the exit node, advertise exit routes and approve them; in the tailnet policy, grant tag:tw-host autogroup:internet; then tw status"

// doctorWorkerInternet is tw doctor's worker_internet check.
func doctorWorkerInternet(s *state) (string, string, string) {
	if s != nil && s.remote() {
		return "skip", "this machine is linked to " + s.Link.Endpoint + ": each host has its own setting (run tw doctor there)", ""
	}
	if s == nil || !s.configured() {
		return "skip", "tw is not set up here", ""
	}
	saved := s.Cfg.workerInternet()
	running, problem := hostWorkerInternet(s)
	d := workerInternetDetail(saved, running, problem)
	switch {
	case running != "" && running != saved:
		return "warn", d, "tw down && tw up"
	case problem != "":
		return "fail", d, exitNodeFix
	case saved == workerInternetOff:
		return "info", d + "; tw up --worker-internet=direct turns it back on", ""
	case strings.HasPrefix(saved, workerInternetExitNode):
		return "ok", d + "; tw up --worker-internet=direct uses this host's network instead", ""
	}
	return "ok", d + "; tw up --worker-internet=off turns it off for this host", ""
}
