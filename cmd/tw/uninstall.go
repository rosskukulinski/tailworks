package main

// tw uninstall (issue #487): one command instead of five, ending in one of
// three places:
//
//   - tw uninstall: nothing of tailworks is left on this machine. Every
//     object is killed and its device deleted (tw down --purge), the local
//     host deregistered (separate-host layout), tailworks stopped and its
//     login service removed, mgmt's own device deleted with the tenant's
//     credential, the coding agents' skills removed, the install prefix
//     removed (or the npm command printed) and the state dir deleted:
//     mgmt.db with every tenant's KV, D1, R2 and queue data, the audit
//     trail and backups/.
//   - tw uninstall --keep-state: the same, but the state dir's data stays
//     (when the state dir is also the prefix, only the install's own files
//     are taken out of it).
//   - a linked developer machine (tw link, no tw up here): link.json, cli/
//     and the prefix go; there is no tailworks to stop.
//
// It refuses first, before it changes anything: when the microVM tier's
// root-owned setup is installed (undoing it needs this binary and sudo),
// when tailworks is set up here but mgmt isn't healthy (the purge needs
// it; --force skips the purge), and when other hosts are registered with
// this mgmt (they would be orphaned). Then it prints exactly what it will
// do and delete, and asks (confirmDestroy; --yes without a terminal).
//
// It never touches the tailnet policy, and it deletes only the entries of
// the state dir and prefix that tailworks writes (docs/INSTALL.md, "What
// is in ~/.tailworks"); anything else in them is left, and named.
//
// install.sh --uninstall --purge stays as the fallback for when no tw
// binary runs; for the prefix it ends in the same place.
//
// How this tw was installed (prefix, npm, npx, source, other) comes from
// installChannel (install_channel.go), as for tw upgrade and tw doctor.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	twagent "github.com/rosskukulinski/tailworks/agent"
	"github.com/rosskukulinski/tailworks/internal/headscale"
	"github.com/rosskukulinski/tailworks/internal/tsapi"
)

// stateEntries are what tailworks writes in the state dir (INSTALL.md's
// "What is in ~/.tailworks" table). Nothing else there is deleted.
var stateEntries = []string{
	"backups",
	"cli",
	"hostd",
	"link.json",
	"logs",
	"mgmt",
	"run",
	"supervisor.pid",
	"tw.json",
	"update-check.json",
	"upgrade",
	"vm",
}

// prefixEntries are what install.sh writes in the prefix (its --uninstall
// list); its .install-* staging dirs are globbed.
var prefixEntries = []string{
	"bin",
	"bin.prev",
	"install.sh",
	"share",
	"share.prev",
	"VERSION",
	"VERSION.prev",
}

// Test seams.
var (
	// uninstallSetupRecord is the microVM tier's root-owned setup record
	// (tw host setup --microvm), on the OS that has one.
	uninstallSetupRecord = func() string {
		if runtime.GOOS == "linux" {
			return "/etc/tailworks/setup.json"
		}
		return ""
	}
	// uninstallChannel is how this binary was installed.
	uninstallChannel = installChannel
	// uninstallDeleteNode deletes one of tailworks' own nodes with the
	// tenant's credential (OAuth client or Headscale API key).
	uninstallDeleteNode = deleteNodeWithCredential
	// uninstallAgentEnv is where the coding agents' skills are.
	uninstallAgentEnv = agentEnv
)

// uninstallResult is `tw uninstall --json` (schema $defs/uninstall).
type uninstallResult struct {
	StateDir  string `json:"stateDir"`
	Prefix    string `json:"prefix,omitempty"`
	KeepState bool   `json:"keepState"`
	DryRun    bool   `json:"dryRun"`
	// Steps are the actions besides deleting files, in order.
	Steps []string `json:"steps"`
	// Remove are the paths it deletes (--dry-run: would delete).
	Remove []string `json:"remove"`
	// Skills are the agent files tailworks' skills are removed from (a
	// file it wrote is deleted; a shared file loses only its tw block).
	Skills []string `json:"skills"`
	// Kept are the paths it leaves on purpose: the state with
	// --keep-state, and anything that isn't tailworks'.
	Kept []string `json:"kept"`
	// Manual is what is left for you to do.
	Manual  []string `json:"manual"`
	Killed  []string `json:"killed"`
	Devices int      `json:"devicesDeleted"`
	Done    bool     `json:"done"`
}

// uninstallPlan is what tw uninstall will do, worked out before it asks.
type uninstallPlan struct {
	res     uninstallResult
	agents  *twagent.Plan
	healthy bool // mgmt answers: purge, deregister the host
	running bool // the supervisor runs: stop it
	rmDirs  []string
}

func cmdUninstall(args []string) error {
	f := newFlags("uninstall")
	keep := f.Bool("keep-state", false, "keep the state dir's data (tw.json, mgmt/, hostd/, backups/, …): remove the install, service, devices and skills only")
	force := f.Bool("force", false, "go on when tailworks is set up here but tw-mgmt isn't running or healthy (the objects' devices are then left: delete them in the admin console)")
	dry := f.Bool("dry-run", false, "print what it would do and delete, and change nothing")
	yes := yesFlag(f)
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return fail("bad_request", "tw uninstall takes no arguments", "usage: tw "+usageOf("uninstall"))
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	if rec := uninstallSetupRecord(); rec != "" && isFile(rec) {
		tw := "tw"
		if ch := uninstallChannel(); ch.Channel == channelPrefix {
			tw = filepath.Join(ch.Prefix, "bin", "tw")
		}
		return fail("uninstall_blocked", "the microVM tier's helper is installed ("+rec+"): undoing it needs root and this tw binary, so it goes first").
			withFix("sudo "+tw+" host setup --undo", "then tw uninstall again")
	}
	if err := uninstallSafeDir(s.Dir); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	running := s.configured() && s.supervisorAlive()
	healthy := running && s.mgmtHealthy(ctx)
	if s.configured() && !healthy && !*force {
		why := "tw is set up in " + s.Dir + " but not running"
		if running {
			why = "tw runs from " + s.Dir + " but tw-mgmt isn't healthy"
		}
		return fail("uninstall_blocked", why+": uninstall needs mgmt to kill every object and delete its device").
			withFix("tw up, then tw uninstall again",
				"or tw uninstall --force   (the objects' devices are left: delete them in the admin console › Machines)")
	}
	if healthy {
		if err := uninstallOtherHosts(ctx, s); err != nil {
			return err
		}
	}
	p := planUninstall(s, *keep, healthy, running)
	p.res.DryRun = *dry
	if !*f.json {
		printUninstallPlan(p)
	}
	if *dry {
		emit(f, p.res, func() {
			printUninstallManual(p)
			fmt.Println("dry run: nothing changed")
		})
		return nil
	}
	what := "tw uninstall deletes what is listed above"
	kill := ""
	if healthy {
		kill = "kills every object and deletes its device, and "
	}
	switch {
	case s.configured() && *keep:
		what = "tw uninstall " + kill + "removes the install (the state dir is kept)"
	case s.configured():
		what = "tw uninstall " + kill + "deletes " + s.Dir + ": mgmt.db (every tenant's KV, D1, R2 and queue data), the audit trail and backups/"
	case !*keep && dirExists(filepath.Join(s.Dir, "backups")):
		what += ", backups/ included"
	}
	var fix []string
	if !*keep && (s.configured() || dirExists(filepath.Join(s.Dir, "backups"))) {
		fix = append(fix, "tw backup --out <a directory outside "+s.Dir+">   (first, if you may want any of it back), or tw uninstall --keep-state")
	}
	cmdline := "tw uninstall"
	if *keep {
		cmdline += " --keep-state"
	}
	if *force {
		cmdline += " --force"
	}
	if err := confirmDestroy(f, *yes, what, "uninstall", cmdline, fix...); err != nil {
		return err
	}
	if err := runUninstall(ctx, s, p); err != nil {
		return err
	}
	p.res.Done = true
	emit(f, p.res, func() { printUninstallDone(p) })
	return nil
}

// uninstallSafeDir refuses a state dir that can't be tailworks' own: the
// root, or the home directory or one of its parents (TW_STATE_DIR=$HOME
// by mistake). Only known entries are deleted anyway, but the dir itself
// is removed when it ends up empty.
func uninstallSafeDir(dir string) error {
	bad := filepath.Dir(dir) == dir
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(dir, home); err == nil && (rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))) {
			bad = true // dir is home, or contains it
		}
	}
	if bad {
		return fail("uninstall_blocked", "the state dir "+dir+" is your home directory, or contains it: tw uninstall won't delete from it").
			withFix("tw uninstall --state-dir <tailworks' own state dir, e.g. ~/.tailworks>")
	}
	return nil
}

// uninstallOtherHosts refuses while hosts other than this machine's are
// registered with this mgmt: they would be left serving nothing, with
// their nodes on the tailnet.
func uninstallOtherHosts(ctx context.Context, s *state) error {
	type hostRow struct {
		HostID string `json:"hostID"`
	}
	hosts, err := listAll[hostRow](ctx, s.mgmt(), "/v1/hosts", 0)
	if err != nil {
		return fail("uninstall_blocked", "can't list this mgmt's hosts: "+err.Error()).withFix("tw host ls", "then tw uninstall again")
	}
	var others []string
	for _, h := range hosts {
		if h.HostID != s.Cfg.HostID {
			others = append(others, h.HostID)
		}
	}
	if len(others) == 0 {
		return nil
	}
	var fix []string
	for _, h := range others {
		fix = append(fix, "tw host drain "+h+" && tw host rm "+h)
	}
	return fail("uninstall_blocked", fmt.Sprintf("other hosts are registered with this mgmt (%s): uninstalling it would orphan them", strings.Join(others, ", "))).
		withFix(append(fix, "then tw uninstall again")...)
}

// uninstallPrefix is the install prefix to remove: the one this binary
// runs from, else the one that runs this state dir. An extracted release
// directory (it has SHA256SUMS) is not an install and is never removed.
func uninstallPrefix(s *state, ch installInfo) string {
	installed := func(p string) bool { return p != "" && !isFile(filepath.Join(p, "SHA256SUMS")) }
	if ch.Channel == channelPrefix && installed(ch.Prefix) {
		return ch.Prefix
	}
	for _, c := range statePrefixCandidates(s) {
		if installed(c.Prefix) {
			return c.Prefix
		}
	}
	return ""
}

// planUninstall works out every step and path before anything changes.
func planUninstall(s *state, keep, healthy, running bool) *uninstallPlan {
	p := &uninstallPlan{healthy: healthy, running: running, res: uninstallResult{StateDir: s.Dir, KeepState: keep,
		Steps: []string{}, Remove: []string{}, Skills: []string{}, Kept: []string{}, Manual: []string{}, Killed: []string{}}}
	r := &p.res
	step := func(f string, a ...any) { r.Steps = append(r.Steps, fmt.Sprintf(f, a...)) }
	manual := func(f string, a ...any) { r.Manual = append(r.Manual, fmt.Sprintf(f, a...)) }

	// The tailworks this state dir runs.
	switch {
	case s.configured():
		if healthy && s.Cfg.TenantID != "" {
			step("kill every object of every tenant and delete its device (tw down --purge)")
		} else if !healthy {
			step("no purge (--force: tw-mgmt isn't running or healthy)")
			manual("delete the objects' devices in the admin console › Machines (tw uninstall couldn't: mgmt wasn't healthy)")
		}
		if !s.Cfg.single() && s.Cfg.HostID != "" {
			if healthy {
				step("deregister this host (%s) and delete its node (tw host rm)", s.Cfg.HostID)
			} else {
				manual("delete this host's node (host %s) in the admin console › Machines", s.Cfg.HostID)
			}
		}
		if running {
			step("stop tailworks (tw down)")
		}
		if serviceIsFor(s) {
			step("remove the login service %s", serviceUnitPath())
		}
		if s.Cfg.MgmtNodeID != "" {
			step("delete mgmt's own node %s (%s) with the tenant's credential", nonEmpty(s.Cfg.MgmtDNSName, s.Cfg.MgmtHostname), s.Cfg.MgmtNodeID)
		}
		if !s.Cfg.storeIsSQLite() {
			db := "the database tw up --db names"
			if s.Cfg.DBFile != "" {
				db = "the database " + s.Cfg.DBFile + " names (and that file)"
			}
			manual("mgmt's store is outside the state dir: drop %s yourself", db)
		}
		if s.Cfg.Custody == "setec" {
			manual("mgmt's secrets are in setec (%s): delete tailworks' entries there", s.Cfg.SetecURL)
		}
		if s.Cfg.oauth() {
			manual("if you're done with tailworks on %s: revoke its OAuth client (admin console › Settings › Trust credentials) and delete %s", s.Cfg.Tailnet, nonEmpty(s.Cfg.OAuthClientFile, "its client file"))
		}
		if s.Cfg.headscale() {
			manual("if you're done with tailworks on this Headscale server: expire its API key (headscale apikeys expire) and delete %s", nonEmpty(s.Cfg.APIKeyFile, "its key file"))
		}
		manual("remove tailworks' tags and grants from the tailnet policy if nothing else uses them (tw uninstall never changes the policy)")
		if runtime.GOOS == "linux" {
			manual("loginctl disable-linger %s   (if you turned lingering on for tw up's service and nothing else needs it)", nonEmpty(os.Getenv("USER"), "$USER"))
		}
	case s.Link != nil:
		step("forget the link to %s (a developer machine: no tailworks runs here)", s.Link.Endpoint)
	}

	// The coding agents' skills: the agents found here, user scope.
	if env, err := uninstallAgentEnv(); err == nil {
		opts := twagent.Options{Version: version, Remove: true, Scope: twagent.ScopeUser}
		for _, d := range twagent.Detect(env) {
			if d.Detected {
				opts.Agents = append(opts.Agents, d.Agent)
			}
		}
		if len(opts.Agents) > 0 {
			if ap, err := twagent.MakePlan(env, opts); err == nil && ap.Pending() > 0 {
				p.agents = ap
				step("remove tailworks' skills (tw agent remove)")
				for _, c := range ap.Changes {
					if c.Action == twagent.Remove || c.Action == twagent.Update {
						x := c.Path
						if c.Kind == "block" || c.Action == twagent.Update {
							x += " (its tw block only)"
						}
						r.Skills = append(r.Skills, x)
					}
				}
			}
		}
	}
	manual("skills set up per project (tw agent setup --scope project) stay: tw agent remove --scope project --dir <project>")

	// The install.
	ch := uninstallChannel()
	prefix := uninstallPrefix(s, ch)
	r.Prefix = prefix
	switch {
	case ch.Channel == channelNPX:
		manual("this tw runs from npx's cache (%s): nothing to uninstall there", ch.NPM.Dir)
	case ch.Channel == channelNPM:
		manual("npm uninstall -g tailworks   (npm owns %s)", ch.NPM.Dir)
	case ch.Channel == channelSource:
		manual("this tw is a source checkout (%s): delete the checkout yourself if you're done with it", ch.Root)
	case prefix == "":
		manual("this tw runs from %s, not an install prefix: remove it yourself if you installed it there", nonEmpty(exeDir(), "an unknown directory"))
	}
	if prefix != "" {
		r.Remove = append(r.Remove, prefixFiles(prefix)...)
		manual("remove the PATH line for %s from your shell profile, if you added one", filepath.Join(prefix, "bin"))
		if !samePath(prefix, s.Dir) || !keep {
			p.rmDirs = append(p.rmDirs, prefix)
		}
	}

	// The state dir.
	if keep {
		r.Kept = append(r.Kept, existing(s.Dir, stateEntries)...)
	} else {
		r.Remove = append(r.Remove, existing(s.Dir, stateEntries)...)
		if s.configured() {
			if rd := s.runDir(); !samePath(filepath.Dir(rd), s.Dir) && dirExists(rd) {
				r.Remove = append(r.Remove, rd) // /tmp/tw-<hash>: the sockets of a long state dir
			}
		}
		if !samePath(prefix, s.Dir) {
			p.rmDirs = append(p.rmDirs, s.Dir)
		}
	}

	// Anything else in the state dir or prefix is not tailworks': it stays.
	dirs := []string{s.Dir}
	if prefix != "" && !samePath(prefix, s.Dir) {
		dirs = append(dirs, prefix)
	}
	for _, d := range dirs {
		ents, _ := os.ReadDir(d)
		for _, e := range ents {
			n := e.Name()
			if !slices.Contains(stateEntries, n) && !slices.Contains(prefixEntries, n) && !strings.HasPrefix(n, ".install-") {
				r.Kept = append(r.Kept, filepath.Join(d, n)+" (not tailworks')")
			}
		}
	}
	return p
}

// prefixFiles are install.sh's files in prefix.
func prefixFiles(prefix string) []string {
	out := existing(prefix, prefixEntries)
	m, _ := filepath.Glob(filepath.Join(prefix, ".install-*"))
	return append(out, m...)
}

func existing(dir string, names []string) []string {
	var out []string
	for _, n := range names {
		if _, err := os.Lstat(filepath.Join(dir, n)); err == nil {
			out = append(out, filepath.Join(dir, n))
		}
	}
	return out
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func printUninstallPlan(p *uninstallPlan) {
	r := p.res
	verb := "tw uninstall will"
	if r.DryRun {
		verb = "tw uninstall would"
	}
	end := "nothing of tailworks is left on this machine"
	if r.KeepState {
		end = "--keep-state: the state in " + r.StateDir + " is kept"
	}
	fmt.Printf("%s (%s):\n", verb, end)
	for _, st := range r.Steps {
		fmt.Println("  · " + st)
	}
	list := func(title string, xs []string) {
		if len(xs) == 0 {
			return
		}
		fmt.Println(title)
		for _, x := range xs {
			fmt.Println("    " + x)
		}
	}
	list("  delete:", r.Remove)
	list("  remove tailworks' skills from:", r.Skills)
	list("  keep:", r.Kept)
}

func printUninstallDone(p *uninstallPlan) {
	r := p.res
	if len(r.Killed) > 0 || r.Devices > 0 {
		fmt.Printf("✓ purged   %d objects killed · %d devices deleted\n", len(r.Killed), r.Devices)
	}
	fmt.Printf("✓ uninstalled tailworks · %d paths deleted", len(r.Remove))
	if r.KeepState {
		fmt.Printf(" · state kept in %s", r.StateDir)
	}
	fmt.Println()
	printUninstallManual(p)
}

func printUninstallManual(p *uninstallPlan) {
	if len(p.res.Manual) == 0 {
		return
	}
	fmt.Println("left for you (tw uninstall doesn't do these):")
	for _, m := range p.res.Manual {
		fmt.Println("  › " + m)
	}
}

// runUninstall does the plan, in order. Past the stop, a step that fails
// is added to Manual and the rest still runs: an uninstall that finishes
// and says what's left beats one that stops in the middle.
func runUninstall(ctx context.Context, s *state, p *uninstallPlan) error {
	r := &p.res
	manual := func(f string, a ...any) { r.Manual = append(r.Manual, fmt.Sprintf(f, a...)) }
	if s.configured() {
		t0 := time.Now()
		dr := downResult{Killed: []string{}, Timings: map[string]float64{}}
		if p.healthy && s.Cfg.TenantID != "" {
			downPurge(ctx, s, &dr) // up.go
			r.Killed, r.Devices = dr.Killed, dr.Flushed
		}
		if p.healthy && !s.Cfg.single() && s.Cfg.HostID != "" {
			var hr struct {
				DevicesLeft []string `json:"devicesLeft"`
			}
			if err := s.mgmt().DoHeaders(ctx, "DELETE", "/v1/hosts/"+s.Cfg.HostID+"?drain=true", nil, &hr, map[string]string{"Tw-Actor": actor()}); err != nil {
				manual("delete this host's node (host %s) in the admin console › Machines (mgmt couldn't: %v)", s.Cfg.HostID, err)
			} else if len(hr.DevicesLeft) > 0 {
				manual("delete the host's own node in the admin console › Machines: %s", strings.Join(hr.DevicesLeft, ", "))
			}
		}
		if p.running {
			if err := downStop(s, true, &dr, t0); err != nil { // up.go
				return err
			}
		}
		if serviceIsFor(s) {
			if _, err := removeService(); err != nil {
				return err
			}
		}
		if id := s.Cfg.MgmtNodeID; id != "" {
			if err := uninstallDeleteNode(ctx, s, id); err != nil {
				where := "the admin console › Machines"
				if s.Cfg.headscale() {
					where = "Headscale (headscale nodes delete)"
				}
				why := err.Error()
				if tsapi.IsForbidden(err) {
					why = "the tenant's OAuth client may not delete devices"
				}
				manual("delete mgmt's node %s (%s) in %s: %s", nonEmpty(s.Cfg.MgmtDNSName, s.Cfg.MgmtHostname), id, where, why)
			} else if r.KeepState {
				// The kept node login is for a device that is gone: the
				// next tw up joins mgmt with a new one.
				s.Cfg.MgmtNodeID = ""
				if err := s.save(); err != nil {
					manual("tw.json still names mgmt's deleted node (%v): tw up may need its node state (mgmt/tsnet) deleted", err)
				}
			}
		}
	}
	if p.agents != nil {
		if err := twagent.Apply(p.agents); err != nil {
			manual("tw agent remove   (removing the skills failed: %v)", err)
		}
	}
	for _, x := range r.Remove {
		if err := os.RemoveAll(x); err != nil {
			manual("delete %s (tw uninstall couldn't: %v)", x, err)
		}
	}
	for _, d := range p.rmDirs {
		os.Remove(d) // only once it is empty: whatever isn't tailworks' stays
	}
	return nil
}

// deleteNodeWithCredential deletes one of tailworks' own nodes through the
// tenant's control API: the Tailscale API with its OAuth client, or
// Headscale's with its API key.
func deleteNodeWithCredential(ctx context.Context, s *state, id string) error {
	switch {
	case s.Cfg.headscale():
		key, err := tsapi.LoadSecretEnvFile(s.Cfg.APIKeyFile, headscale.APIKeyEnv)
		if err != nil {
			return err
		}
		return (&headscale.Client{BaseURL: s.Cfg.ControlURL, APIKey: key, BaseDomain: s.Cfg.Tailnet}).DeleteDevice(ctx, id)
	case s.oauthAPI() != nil:
		return s.oauthAPI().DeleteDevice(ctx, id)
	}
	return errors.New("tw.json names no tenant credential")
}
