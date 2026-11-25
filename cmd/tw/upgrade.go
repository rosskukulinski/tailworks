package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/rosskukulinski/tailworks/internal/catalog"
	"github.com/rosskukulinski/tailworks/internal/releasesig"
	twversion "github.com/rosskukulinski/tailworks/internal/version"
	"github.com/rosskukulinski/tailworks/mgmt"
)

// tw upgrade (ADR-041 §8).
//
//   - On a `tw up` machine (homelab): download (or take --from) a release,
//     verify it, show the plan from its catalog, back up mgmt's store with
//     custody, stop, install (install.sh keeps the previous release as
//     .prev), start, health-check, and roll back automatically if the
//     health check fails and no contract migration ran.
//   - Against a linked tw-mgmt (`tw link`), or as `tw upgrade fleet`: ask
//     mgmt for a plan and start an upgrade run that mgmt drives (tw upgrade
//     fleet status|pause|resume|abort; upgrade_fleet.go).
//   - tw upgrade import|releases (upgrade_import.go) feed mgmt's release
//     catalog; tw upgrade rollback (upgrade_rollback.go) puts the previous
//     release back by hand.

const upgradeUsage = "upgrade [<version>] [--from <tarball|dir>] [--plan] [--yes] [--allow-unsigned] | " +
	"upgrade fleet [<version>] [--plan] [--yes] [--hosts <host>,…] [--canary <host>] [--soak <d>] [--settle <d>] [--no-wait] | " +
	"upgrade fleet status|pause|resume|abort [<run-id>] | " +
	"upgrade import <tarball>…|<version> [--platform <os>-<arch>,…] | " +
	"upgrade releases | " +
	"upgrade rollback [<backup-dir>] [--restore] [--prefix <dir>] [--yes]"

func cmdUpgrade(args []string) error {
	sub, rest := upgradeSubcommand(args, append([]string{"fleet", "import", "releases", "rollback"}, upgradeRunActions...)...) // upgrade_fleet.go
	switch sub {
	case "fleet":
		return cmdUpgradeFleet(rest)
	case "import":
		return cmdUpgradeImport(rest) // upgrade_import.go
	case "releases":
		return cmdUpgradeReleases(rest)
	case "rollback":
		return cmdUpgradeRollback(rest) // upgrade_rollback.go
	case "":
	default: // status, pause, resume, abort: the old spelling
		upgradeDeprecated(rest, "tw upgrade "+sub, "tw upgrade fleet "+sub)
		return cmdUpgradeRun(sub, rest)
	}
	f := newFlags("upgrade")
	from := f.String("from", "", "install this release tarball (tw-<v>-<os>-<arch>.tar.gz, with SHA256SUMS next to it) or extracted release directory instead of downloading")
	planOnly := f.Bool("plan", false, "print the plan and change nothing")
	yes := yesFlagBefore(f, "upgrading")
	allowUnsigned := f.Bool("allow-unsigned", false, "install a release that isn't signed by a pinned key (a build you made; TW_ALLOW_UNSIGNED=1)")
	health := f.Duration("health-timeout", 3*time.Minute, "how long the upgraded tw may take to report healthy before it is rolled back")
	noRollback := f.Bool("no-rollback", false, "keep the new release even if the health check fails")
	prefixFlag := f.String("prefix", "", "the install.sh prefix to upgrade (default: the one that owns --state-dir)")
	f.group(advancedFlags, "health-timeout", "no-rollback", "prefix")
	fleet := f.Bool("fleet", false, "upgrade the fleet through tw-mgmt (now tw upgrade fleet)")
	fo := upgradeFleetFlags(f, planOnly, yes)
	f.hidden("for one release (now tw upgrade fleet)", append([]string{"fleet"}, fo.names...)...)
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	want, err := upgradeWantVersion(pos, "tw upgrade")
	if err != nil {
		return err
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	localOnly := setFlags(f, []string{"allow-unsigned", "from", "health-timeout", "no-rollback", "prefix"})
	fleetOnly := setFlags(f, fo.names)
	if *fleet || s.remote() {
		if len(localOnly) > 0 {
			return fail("bad_request", strings.Join(localOnly, ", ")+": only for upgrading the tw up machine itself; this upgrades the fleet through tw-mgmt").
				withFix("tw upgrade fleet [<version>] [--plan]   (the fleet's flags: tw upgrade fleet --help)")
		}
		if *fleet {
			upgradeDeprecated(args, "tw upgrade --fleet", "tw upgrade fleet")
		}
		return fleetUpgrade(f, s, fo.request(want), *planOnly, *yes, *fo.noWait)
	}
	if len(fleetOnly) > 0 {
		return fail("bad_request", strings.Join(fleetOnly, ", ")+": only for a fleet upgrade run").
			withFix("tw upgrade fleet [<version>] " + strings.Join(fleetOnly, " ") + " …")
	}
	return homelabUpgrade(f, s, want, *from, *prefixFlag, *planOnly, *yes, *health, *noRollback,
		*allowUnsigned || allowUnsignedEnv())
}

func splitComma(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func askYes(f *flags, question string, yes bool, what, planCmd string) error {
	if yes {
		return nil
	}
	if *f.json || !isTerminal() {
		return fail("confirmation_required", what+"; it asks first").
			withFix(planCmd+"   (shows what it would do)", "then run it in a terminal, or add --yes")
	}
	fmt.Printf("%s [y/N] ", question)
	ln, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if a := strings.ToLower(strings.TrimSpace(ln)); a != "y" && a != "yes" {
		return fail("cancelled", "nothing was changed")
	}
	return nil
}

// ---- fleet ----

func fleetUpgrade(f *flags, s *state, req mgmt.UpgradeRequest, planOnly, yes, noWait bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := s.mgmt()
	hdr := map[string]string{"Tw-Actor": actor()}
	if req.To == "" {
		var cat struct {
			Current string              `json:"current"`
			Items   []mgmt.ReleaseEntry `json:"items"`
		}
		if err := c.DoHeaders(ctx, "GET", "/v1/releases", nil, &cat, hdr); err != nil {
			return err
		}
		if len(cat.Items) == 0 {
			return fail("bad_request", "tw-mgmt's release catalog is empty").withFix("tw upgrade import <version>   (or the release tarballs: tw upgrade import tailworks-<version>-<os>-<arch>.tar.gz …)")
		}
		req.To = cat.Items[len(cat.Items)-1].Version
	}
	req.DryRun = true
	var plan mgmt.UpgradePlan
	if err := c.DoHeaders(ctx, "POST", "/v1/upgrades", req, &plan, hdr); err != nil {
		return err
	}
	if planOnly {
		emit(f, plan, func() { printFleetPlan(plan) })
		return nil
	}
	if len(plan.Blockers) > 0 {
		if !*f.json {
			printFleetPlan(plan)
		}
		return fail("upgrade_blocked", "the upgrade can't start: "+strings.Join(plan.Blockers, "; ")).withFix("tw upgrade fleet --plan " + req.To)
	}
	if !yes && !*f.json && isTerminal() {
		printFleetPlan(plan)
	}
	if err := askYes(f, "Start the upgrade run to "+req.To+"?", yes, "tw upgrade fleet would start a fleet upgrade run to "+req.To, "tw upgrade fleet --plan "+req.To); err != nil {
		return err
	}
	req.DryRun = false
	var run mgmt.UpgradeRun
	if err := c.DoHeaders(ctx, "POST", "/v1/upgrades", req, &run, hdr); err != nil {
		return err
	}
	if noWait {
		emit(f, run, func() {
			fmt.Printf("✓ upgrade run %s started (to %s); follow it: tw upgrade fleet status %s\n", run.ID, run.To, run.ID)
		})
		return nil
	}
	return followRun(f, s, run.ID)
}

func printFleetPlan(p mgmt.UpgradePlan) {
	fmt.Printf("Upgrade to tailworks %s\n", p.To)
	fmt.Printf("  mgmt replicas run: %s\n", strings.Join(orNone(p.From.Mgmt), ", "))
	for _, st := range p.Steps {
		line := "  · " + st.Kind
		if st.Host != "" {
			line += " " + st.Host
		}
		if st.Canary {
			line += " (canary)"
		}
		if st.Method != "" {
			line += " [" + st.Method + "]"
		}
		line += ": " + st.Status
		if st.Detail != "" {
			line += " — " + st.Detail
		}
		fmt.Println(line)
		if st.NeedsOperator != "" {
			fmt.Println("      operator: " + st.NeedsOperator)
		}
	}
	printNotes("Breaking changes", p.Breaking)
	printNotes("Operator actions", p.Actions)
	for _, w := range p.Warnings {
		fmt.Println("  ! " + w)
	}
	for _, b := range p.Blockers {
		fmt.Println("  ✗ " + b)
	}
}

func orNone(xs []string) []string {
	if len(xs) == 0 {
		return []string{"(none)"}
	}
	return xs
}

func printNotes(title string, ns []catalog.Note) {
	if len(ns) == 0 {
		return
	}
	fmt.Println(title + ":")
	for _, n := range ns {
		text := strings.TrimSpace(n.Text)
		if i := strings.IndexByte(text, '\n'); i > 0 {
			text = text[:i] + " …"
		}
		fmt.Printf("  - %s (%s)\n", text, n.Source)
	}
}

func cmdUpgradeRun(action string, args []string) error {
	f := newFlags("upgrade " + action)
	pos, err := f.parse(args)
	if err != nil {
		return err
	}
	s, err := openState(*f.stateDir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c := s.mgmt()
	hdr := map[string]string{"Tw-Actor": actor()}
	id := ""
	if len(pos) > 0 {
		id = pos[0]
	} else {
		var runs struct {
			Items []mgmt.UpgradeRun `json:"items"`
		}
		if err := c.DoHeaders(ctx, "GET", "/v1/upgrades", nil, &runs, hdr); err != nil {
			return err
		}
		if len(runs.Items) == 0 {
			if action == "status" {
				emit(f, map[string]any{"runs": []any{}}, func() { fmt.Println("no upgrade runs") })
				return nil
			}
			return fail("not_found", "no upgrade run")
		}
		id = runs.Items[0].ID // newest
	}
	if action == "status" {
		var run mgmt.UpgradeRun
		if err := c.DoHeaders(ctx, "GET", "/v1/upgrades/"+id, nil, &run, hdr); err != nil {
			return err
		}
		emit(f, run, func() { printRun(run) })
		return nil
	}
	var run mgmt.UpgradeRun
	if err := c.DoHeaders(ctx, "POST", "/v1/upgrades/"+id+"/actions", map[string]string{"action": action}, &run, hdr); err != nil {
		return err
	}
	emit(f, run, func() { fmt.Printf("✓ upgrade run %s: %s\n", run.ID, run.State) })
	return nil
}

func printRun(run mgmt.UpgradeRun) {
	fmt.Printf("upgrade run %s → tw %s: %s", run.ID, run.To, run.State)
	if run.Reason != "" {
		fmt.Printf(" (%s)", run.Reason)
	}
	fmt.Println()
	for i, st := range run.Steps {
		mark := "  "
		if i == run.Step && (run.State == "running" || run.State == "paused") {
			mark = "▸ "
		}
		line := mark + st.Kind
		if st.Host != "" {
			line += " " + st.Host
		}
		line += ": " + st.Status
		if st.Phase != "" && st.Status != "done" {
			line += " (" + st.Phase + ")"
		}
		if st.Detail != "" {
			line += " — " + st.Detail
		}
		fmt.Println(line)
		if st.NeedsOperator != "" {
			fmt.Println("    operator: " + st.NeedsOperator)
		}
	}
}

// followRun polls a run until it stops running.
func followRun(f *flags, s *state, id string) error {
	c := s.mgmt()
	last := ""
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		var run mgmt.UpgradeRun
		err := c.DoHeaders(ctx, "GET", "/v1/upgrades/"+id, nil, &run, map[string]string{"Tw-Actor": actor()})
		cancel()
		if err != nil {
			return err
		}
		if !*f.json {
			cur := fmt.Sprintf("%s %d", run.State, run.Step)
			if run.Step < len(run.Steps) {
				st := run.Steps[run.Step]
				cur += " " + st.Host + " " + st.Phase + " " + st.Detail + " " + st.NeedsOperator
				if cur != last {
					fmt.Printf("· %s %s %s %s\n", st.Kind, st.Host, st.Phase, st.Detail)
					if st.NeedsOperator != "" {
						fmt.Println("  operator: " + st.NeedsOperator)
					}
				}
			}
			last = cur
		}
		switch run.State {
		case "running":
			time.Sleep(3 * time.Second)
			continue
		case "done":
			emit(f, run, func() { fmt.Printf("✓ upgrade run %s: every component runs tw %s\n", run.ID, run.To) })
			return nil
		default:
			if !*f.json {
				printRun(run)
			}
			return fail("upgrade_"+run.State, fmt.Sprintf("upgrade run %s is %s: %s", run.ID, run.State, run.Reason)).
				withFix("fix the cause, then: tw upgrade fleet resume "+run.ID, "or: tw upgrade fleet abort "+run.ID)
		}
	}
}

// ---- homelab ----

// localPlan is `tw upgrade --plan` on a tw up machine.
type localPlan struct {
	From               string         `json:"from"`
	To                 string         `json:"to"`
	Source             string         `json:"source"`
	Breaking           []catalog.Note `json:"breaking"`
	Actions            []catalog.Note `json:"actions"`
	ContractMigrations []string       `json:"contractMigrations"`
	Workerd            string         `json:"workerd,omitempty"`
	Steps              []string       `json:"steps"`
	Warnings           []string       `json:"warnings"`
}

type localResult struct {
	Plan       localPlan `json:"plan"`
	Upgraded   bool      `json:"upgraded"`
	Backup     string    `json:"backup,omitempty"`
	RolledBack bool      `json:"rolledBack,omitempty"`
	Health     string    `json:"health,omitempty"`
	// NotActiveBefore are deployments that weren't active before the
	// upgrade: the health check didn't count them (issue #63).
	NotActiveBefore []string `json:"notActiveBefore,omitempty"`

	// AgentSkills is what the new tw's agent setup --refresh did (issue #490).
	AgentSkills string `json:"agentSkills,omitempty"`
}

func homelabUpgrade(f *flags, s *state, want, from, prefixFlag string, planOnly, yes bool, healthTimeout time.Duration, noRollback bool,
	allowUnsigned bool) error {
	pc, err := resolvePrefix(prefixFlag, installPrefixOf(), statePrefixCandidates(s))
	if err != nil {
		return err
	}
	prefix := pc.Prefix
	if in, npm := npmSelf(); npm && prefix == "" {
		if planOnly {
			return npmUpgradePlan(f, installChannel(), want) // install_channel.go
		}
		return npmUpgradeErr(in) // npm_install.go
	}
	if prefix == "" && !planOnly {
		return fail("bad_request", "this tailworks doesn't run from an install.sh prefix (bin/, share/tailworks, VERSION), so it can't upgrade itself in place").
			withFix(installChannel().upgradeCommand(want), // install_channel.go
				"or install a release by hand: tar -xzf tw-<v>-…tar.gz && ./tw-<v>-…/install.sh",
				"or name the install to upgrade: tw upgrade --prefix <dir>")
	}
	if pc.Mismatch != "" && !planOnly {
		return fail("prefix_mismatch", pc.Mismatch+": tw upgrade won't guess which one to replace").
			withFix("tw upgrade --prefix "+prefix+" …   (upgrade the install that runs the state dir)",
				"or run the tailworks installed there: "+filepath.Join(prefix, "bin", "tw")+" upgrade …")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	stage := filepath.Join(s.Dir, "upgrade")
	os.RemoveAll(stage)
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	rel, source, err := fetchRelease(ctx, want, from, stage)
	if err != nil {
		return err
	}
	sigWarning, err := verifyUpgradeRelease(rel, allowUnsigned) // before anything in it runs (upgradesig.go)
	if err != nil {
		return err
	}
	meta, err := readReleaseMeta(rel)
	if err != nil {
		return err
	}
	if meta.OS != runtime.GOOS || meta.Arch != runtime.GOARCH {
		return fail("bad_request", fmt.Sprintf("the release is for %s-%s, this machine is %s-%s", meta.OS, meta.Arch, runtime.GOOS, runtime.GOARCH))
	}
	cur := installedVersion(prefix)
	plan := localPlanFor(cur, meta, rel, source, s)
	if sigWarning != "" {
		plan.Warnings = append(plan.Warnings, sigWarning)
	}
	if want != "" && meta.Version != want {
		return fail("bad_request", fmt.Sprintf("the release is tailworks %s, not the %s you asked for", meta.Version, want))
	}
	if pc.Mismatch != "" {
		plan.Warnings = append(plan.Warnings, pc.Mismatch+": the run needs --prefix "+prefix)
	}
	if prefix == "" {
		plan.Warnings = append(plan.Warnings, noPrefixUpgrade(want)) // install_channel.go
	}
	if s.supervisorAlive() {
		plan.Warnings = append(plan.Warnings, notActiveWarnings(snapshotDeployments(s))...)
	}
	res := localResult{Plan: plan}
	if planOnly {
		emit(f, res, func() { printLocalPlan(plan) })
		return nil
	}
	if tv, err := twversion.Parse(meta.Version); err == nil {
		if cv, err := twversion.Parse(cur); err == nil && tv.Compare(cv) <= 0 {
			emit(f, res, func() { fmt.Printf("tw %s is installed; the release is %s: nothing to upgrade\n", cur, meta.Version) })
			return nil
		}
	}
	if !*f.json && !yes && isTerminal() {
		printLocalPlan(plan)
	}
	if err := askYes(f, "Upgrade tailworks "+cur+" → "+meta.Version+"?", yes, "tw upgrade would replace tw "+cur+" with "+meta.Version, "tw upgrade --plan"); err != nil {
		return err
	}

	say := func(format string, a ...any) {
		if !*f.json {
			fmt.Printf(format+"\n", a...)
		}
	}
	// 1. Back up the store with custody (they are only valid together).
	dsn, _ := s.Cfg.storeDSN(s.Dir) // up_store.go
	before, _ := mgmt.CheckSchema(ctx, dsn)
	backup, err := backupMgmt(ctx, s, cur)
	if err != nil {
		return fail("upgrade_failed", "backup: "+err.Error())
	}
	res.Backup = backup
	say("✓ backup   %s", backup)

	// 2. Stop, install, start. What was broken before isn't held against
	// the new release by the health check.
	wasService, wasRunning := restartMode(s)
	var prior deploymentsSnapshot
	if wasService || wasRunning {
		prior = snapshotDeployments(s)
		res.NotActiveBefore = wasFailedBefore(prior)
		for _, l := range res.NotActiveBefore {
			say("! %s: the health check won't count it", l)
		}
	}
	if err := stopTw(s, prefix, wasService, wasRunning); err != nil {
		return err
	}
	instArgs := []string{filepath.Join(rel, "install.sh"), "--prefix", prefix, "--force"}
	if allowUnsigned {
		instArgs = append(instArgs, "--allow-unsigned")
	}
	inst := exec.CommandContext(ctx, "sh", instArgs...)
	if out, err := inst.CombinedOutput(); err != nil {
		startTw(s, prefix, wasService, wasRunning) // the old release is still in place
		return fail("upgrade_failed", "install.sh failed: "+err.Error(), lastLinesOf(string(out), 5)...)
	}
	say("✓ installed tw %s (tw %s kept as %s)", meta.Version, cur, filepath.Join(prefix, "bin.prev"))
	if err := startTw(s, prefix, wasService, wasRunning, "--apply-policy=false"); err != nil {
		say("! start failed: %v", err)
	}

	// 3. Health check.
	if wasService || wasRunning {
		err = waitHealthy(s, meta.Version, healthTimeout, prior)
	}
	if err == nil {
		res.Upgraded, res.Health = true, "ok"
		res.AgentSkills = refreshAfterUpgrade(plan, prefix, say) // agent_refresh.go
		emit(f, res, func() {
			fmt.Printf("✓ tw %s is up and healthy\n", meta.Version)
			for _, l := range res.NotActiveBefore {
				fmt.Println("  ! " + l + " (not checked; `tw deploy` it again)")
			}
			if res.AgentSkills != "" {
				fmt.Println("  ✓ " + res.AgentSkills)
			}
			fmt.Println("  project agent skills (tw agent setup --scope project) keep the old version: re-run it in those projects")
		})
		return nil
	}
	res.Health = err.Error()
	after, _ := mgmt.CheckSchema(ctx, dsn)
	contracted := after.MinBinary != "" && after.MinBinary != before.MinBinary
	if noRollback || contracted {
		why := "--no-rollback"
		if contracted {
			why = "a contract migration ran (the old tailworks can't open the store)"
		}
		return fail("upgrade_failed", fmt.Sprintf("tw %s did not become healthy (%v); not rolled back: %s", meta.Version, err, why)).
			withFix("tw logs --component mgmt   and   tw doctor",
				"to go back: tw upgrade rollback "+backup+" --yes   (puts tw "+cur+" and the store from before the upgrade back)")
	}
	say("! tw %s did not become healthy (%v): rolling back to %s", meta.Version, err, cur)
	if rerr := rollback(ctx, s, prefix, backup, wasService, wasRunning); rerr != nil {
		return fail("upgrade_failed", fmt.Sprintf("health check failed (%v) and the rollback failed too: %v", err, rerr)).
			withFix("if "+filepath.Join(prefix, "VERSION")+" still says "+meta.Version+": tw upgrade rollback "+backup+" --yes",
				"if it says "+cur+" (the release went back, the store didn't): tw down; tw restore "+backup+" --yes; tw up")
	}
	res.RolledBack = true
	if *f.json {
		emit(f, res, func() {})
	}
	return fail("upgrade_failed", fmt.Sprintf("tw %s did not become healthy (%v); rolled back to %s, store restored from %s", meta.Version, err, cur, backup)).
		withFix("tw logs --component mgmt   (why the new release failed)")
}

// installPrefixOf is the install.sh prefix this tailworks binary runs from, or
// "". It is not necessarily the install tw upgrade replaces (resolvePrefix).
func installPrefixOf() string {
	if p := filepath.Dir(exeDir()); isInstallPrefix(p) {
		return p
	}
	return ""
}

func installedVersion(prefix string) string {
	if b, err := os.ReadFile(filepath.Join(prefix, "VERSION")); err == nil {
		return strings.TrimSpace(string(b))
	}
	return version
}

type releaseMeta struct {
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
	Workerd string `json:"workerd"`
}

func readReleaseMeta(dir string) (releaseMeta, error) {
	var m releaseMeta
	b, err := os.ReadFile(filepath.Join(dir, "share", "tailworks", "release.json"))
	if err != nil {
		return m, fail("bad_request", dir+" is not a tw release (no share/tailworks/release.json)")
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fail("bad_request", "release.json: "+err.Error())
	}
	return m, nil
}

func localPlanFor(cur string, meta releaseMeta, rel, source string, s *state) localPlan {
	p := localPlan{From: cur, To: meta.Version, Source: source, Breaking: []catalog.Note{}, Actions: []catalog.Note{},
		ContractMigrations: []string{}, Warnings: []string{}}
	es := catalog.Embedded()
	if b, err := os.ReadFile(filepath.Join(rel, "share", "tailworks", "catalog.json")); err == nil {
		if more, err := catalog.Parse(b); err == nil {
			es = mergeEntries(es, more)
		}
	} else {
		p.Warnings = append(p.Warnings, "the release has no catalog (share/tailworks/catalog.json): no release notes to show")
	}
	from := cur
	if _, err := twversion.Parse(from); err != nil {
		from = twversion.Legacy
	}
	between := catalog.Between(es, from, meta.Version)
	p.Breaking = append(p.Breaking, catalog.Breaking(between)...)
	for _, e := range between {
		p.ContractMigrations = append(p.ContractMigrations, e.ContractMigrations...)
		for _, n := range e.Notes {
			if n.Kind == "breaking" {
				continue
			}
			act := strings.Contains(n.Text, "Operator action:")
			for _, k := range n.Upgrade {
				act = act || slices.Contains([]string{"schema-contract", "workerd", "config-default", "compat-date"}, k)
			}
			if act {
				p.Actions = append(p.Actions, n)
			}
		}
	}
	if meta.Workerd != "" && meta.Workerd != twversion.WorkerdPin {
		p.Workerd = twversion.WorkerdPin + " → " + meta.Workerd
	}
	p.Steps = []string{
		"back up " + filepath.Join(s.Dir, "mgmt") + " (store + custody) into " + filepath.Join(s.Dir, "backups"),
		"stop tw",
		"install tw " + meta.Version + " (the current release is kept as bin.prev / share.prev)",
		"start tailworks and wait until it is healthy",
		"if it isn't: roll back to " + cur + " and restore the store (unless a contract migration ran)",
	}
	if inst, _ := serviceOf(s); !inst && !s.supervisorAlive() {
		// No health check then, so nothing is rolled back (homelabUpgrade).
		p.Steps = append(p.Steps[:3], "tw is not running: install only (no health check, no automatic rollback)")
	}
	if st := agentRefreshStep(); st != "" { // agent_refresh.go
		p.Steps = append(p.Steps, st)
	}
	if runtime.GOOS == "linux" {
		if _, err := os.Stat("/etc/tailworks/setup.json"); err == nil {
			p.Warnings = append(p.Warnings, "the microVM tier's helper is root-owned: after the upgrade run `sudo "+filepath.Join(filepathDirOr(exeDir()), "tw")+" host setup --microvm` if tw doctor says its protocol is out of range")
		}
	}
	return p
}

func filepathDirOr(d string) string {
	if d == "" {
		return "."
	}
	return d
}

func printLocalPlan(p localPlan) {
	fmt.Printf("Upgrade tailworks %s → %s (%s)\n", p.From, p.To, p.Source)
	for i, st := range p.Steps {
		fmt.Printf("  %d. %s\n", i+1, st)
	}
	if p.Workerd != "" {
		fmt.Println("  workerd: " + p.Workerd)
	}
	if len(p.ContractMigrations) > 0 {
		fmt.Println("  contract migrations (no automatic rollback after these): " + strings.Join(p.ContractMigrations, ", "))
	}
	printNotes("Breaking changes", p.Breaking)
	printNotes("Operator actions", p.Actions)
	for _, w := range p.Warnings {
		fmt.Println("  ! " + w)
	}
}

// fetchRelease puts the release's extracted directory under stage and
// says where it came from.
func fetchRelease(ctx context.Context, want, from, stage string) (dir, source string, err error) {
	if from != "" {
		st, err := os.Stat(from)
		if err != nil {
			return "", "", fail("bad_request", err.Error())
		}
		if st.IsDir() {
			return from, from, nil
		}
		if sums, err := os.ReadFile(filepath.Join(filepath.Dir(from), "SHA256SUMS")); err == nil {
			if err := checkSum(from, sums); err != nil {
				return "", "", err
			}
		} else {
			return "", "", fail("bad_request", "no SHA256SUMS next to "+from+": tw upgrade verifies a release before installing it").
				withFix("put the release's SHA256SUMS next to the tarball, or extract it and pass the directory")
		}
		d, err := extractRelease(from, stage)
		return d, from, err
	}
	repo := cmpOr(os.Getenv("TW_REPO"), "rosskukulinski/tailworks")
	tag := ""
	if want == "" {
		tag, err = latestTag(ctx, repo)
		if err != nil {
			return "", "", err
		}
	} else {
		tag = "v" + want
	}
	v := strings.TrimPrefix(tag, "v")
	name := fmt.Sprintf("tailworks-%s-%s-%s.tar.gz", v, runtime.GOOS, runtime.GOARCH)
	base := cmpOr(os.Getenv("TW_RELEASE_BASE"), "https://github.com/"+repo+"/releases/download/"+tag)
	tgz := filepath.Join(stage, name)
	if err := download(ctx, base+"/"+name, tgz); err != nil {
		return "", "", err
	}
	sumsPath := filepath.Join(stage, "SHA256SUMS")
	if err := download(ctx, base+"/SHA256SUMS", sumsPath); err != nil {
		return "", "", err
	}
	sums, _ := os.ReadFile(sumsPath)
	if err := checkSum(tgz, sums); err != nil {
		return "", "", err
	}
	d, err := extractRelease(tgz, stage)
	return d, base + "/" + name, err
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func latestTag(ctx context.Context, repo string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", releaseAPI()+"/repos/"+repo+"/releases/latest", nil) // updatecheck.go
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fail("network", "finding the latest release: "+err.Error())
	}
	defer resp.Body.Close()
	var r struct {
		Tag string `json:"tag_name"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&r) != nil || r.Tag == "" {
		return "", fail("not_found", "no published tw release found in "+repo).withFix("tw upgrade <version>   or   tw upgrade --from <tarball>")
	}
	return r.Tag, nil
}

func download(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fail("network", "download "+url+": "+err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fail("not_found", "download "+url+": "+resp.Status)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, io.LimitReader(resp.Body, 2<<30))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

func checkSum(path string, sums []byte) error {
	name := filepath.Base(path)
	want := ""
	for _, line := range strings.Split(string(sums), "\n") {
		fs := strings.Fields(line)
		if len(fs) == 2 && strings.TrimPrefix(fs[1], "*") == name {
			want = fs[0]
		}
	}
	if want == "" {
		return fail("bad_request", "SHA256SUMS has no entry for "+name)
	}
	fh, err := os.Open(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fail("bad_request", fmt.Sprintf("checksum mismatch for %s (got %s, want %s)", name, got, want))
	}
	return nil
}

// extractRelease unpacks a release tarball into stage/x and returns its
// tailworks-<v>-<os>-<arch> directory. It uses the hardened extractor
// tw-hostd uses (releasesig.ExtractTarGz): it runs before the signature
// check, on input that may be hostile.
func extractRelease(tgz, stage string) (string, error) {
	root := filepath.Join(stage, "x")
	if err := releasesig.ExtractTarGz(tgz, root); err != nil {
		return "", fail("bad_request", filepath.Base(tgz)+": "+err.Error())
	}
	ds, _ := filepath.Glob(filepath.Join(root, "tailworks-*"))
	if len(ds) != 1 {
		return "", fail("bad_request", tgz+" is not a tw release tarball")
	}
	return ds[0], nil
}

// backupMgmt copies the store (VACUUM INTO) and the rest of mgmt's state
// that must match it (custody, ID secret, node state), plus
// tw.json, into <state>/backups/<time>-<version>.
func backupMgmt(ctx context.Context, s *state, cur string) (string, error) {
	return backupMgmtTo(ctx, s, filepath.Join(s.Dir, "backups", time.Now().UTC().Format("20060102T150405Z")+"-"+cur))
}

// backupMgmtTo is backupMgmt into dst (tw backup --out).
func backupMgmtTo(ctx context.Context, s *state, dst string) (string, error) {
	if err := os.MkdirAll(filepath.Join(dst, "mgmt"), 0o700); err != nil {
		return "", err
	}
	src := filepath.Join(s.Dir, "mgmt")
	// A Postgres store is backed up with pg_dump (docs/postgres.md); a
	// mgmt.db left from before the move is not the store.
	if _, err := os.Stat(filepath.Join(src, "mgmt.db")); err == nil && s.Cfg.storeIsSQLite() {
		if err := mgmt.BackupSQLite(ctx, filepath.Join(src, "mgmt.db"), filepath.Join(dst, "mgmt", "mgmt.db")); err != nil {
			return "", fmt.Errorf("store: %w", err)
		}
	}
	skip := map[string]bool{"mgmt.db": true, "mgmt.db-wal": true, "mgmt.db-shm": true, "registry": true, "releases": true, "logs": true}
	ents, _ := os.ReadDir(src)
	for _, e := range ents {
		if skip[e.Name()] {
			continue
		}
		if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, "mgmt", e.Name())); err != nil {
			return "", err
		}
	}
	if _, err := os.Stat(filepath.Join(s.Dir, "tw.json")); err == nil {
		if err := copyTree(filepath.Join(s.Dir, "tw.json"), filepath.Join(dst, "tw.json")); err != nil {
			return "", err
		}
	}
	return dst, nil
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		switch {
		case info.IsDir():
			return os.MkdirAll(t, 0o700)
		case info.Mode().IsRegular():
			in, err := os.Open(p)
			if err != nil {
				return err
			}
			defer in.Close()
			out, err := os.OpenFile(t, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
			if err != nil {
				return err
			}
			_, err = io.Copy(out, in)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			return err
		}
		return nil // sockets, symlinks: not state
	})
}

// restoreMgmt puts a backup back (tailworks must be stopped).
func restoreMgmt(s *state, backup string) error {
	dst := filepath.Join(s.Dir, "mgmt")
	if fileExists(filepath.Join(backup, "mgmt", "mgmt.db")) { // not a Postgres install's backup
		for _, f := range []string{"mgmt.db", "mgmt.db-wal", "mgmt.db-shm"} {
			os.Remove(filepath.Join(dst, f))
		}
	}
	ents, err := os.ReadDir(filepath.Join(backup, "mgmt"))
	if err != nil {
		return err
	}
	for _, e := range ents {
		os.RemoveAll(filepath.Join(dst, e.Name()))
		if err := copyTree(filepath.Join(backup, "mgmt", e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(backup, "tw.json")); err == nil {
		return copyTree(filepath.Join(backup, "tw.json"), filepath.Join(s.Dir, "tw.json"))
	}
	return nil
}

func restartMode(s *state) (service, running bool) {
	inst, act := serviceOf(s)
	if inst && act {
		return true, false
	}
	return false, s.supervisorAlive()
}

func stopTw(s *state, prefix string, service, running bool) error {
	switch {
	case service:
		return serviceStop()
	case running:
		out, err := exec.Command(filepath.Join(prefix, "bin", "tw"), "down", "--state-dir", s.Dir).CombinedOutput()
		if err != nil {
			return fail("upgrade_failed", "tw down: "+err.Error(), lastLinesOf(string(out), 5)...)
		}
	}
	return nil
}

// startTw starts tw again the way it ran. upFlags go to a plain `tw up`
// restart: the new release gets --apply-policy=false (an unattended
// restart never asks about or writes the tailnet policy); a restored or
// still-installed older release may not know that flag.
func startTw(s *state, prefix string, service, running bool, upFlags ...string) error {
	switch {
	case service:
		if runtime.GOOS == "darwin" {
			_, err := launchctl("kickstart", "-k", launchdDomain()+"/"+launchdLabel)
			return err
		}
		_, err := systemctlUser("restart", serviceUnit)
		return err
	case running:
		cmd := exec.Command(filepath.Join(prefix, "bin", "tw"), append([]string{"up", "--yes", "--state-dir", s.Dir}, upFlags...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("tw up: %v: %s", err, strings.Join(lastLinesOf(string(out), 3), " | "))
		}
	}
	return nil
}

// waitHealthy waits for mgmt to answer with every replica and host on want
// and every deployment that was active before (before) active again.
func waitHealthy(s0 *state, want string, timeout time.Duration, before deploymentsSnapshot) error {
	deadline := time.Now().Add(timeout)
	last := "not answering"
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)
		s, err := openState(s0.Dir)
		if err != nil {
			last = err.Error()
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var st struct {
			OK       bool               `json:"ok"`
			Versions mgmt.VersionStatus `json:"versions"`
		}
		err = s.mgmt().Do(ctx, "GET", "/v1/status", nil, &st)
		cancel()
		if err != nil {
			last = "mgmt: " + err.Error()
			continue
		}
		if why := versionsAt(st.Versions, want); why != "" {
			last = why
			continue
		}
		if !st.OK {
			last = "GET /v1/status is not ok"
			continue
		}
		if why := deploymentsActive(s, before); why != "" {
			last = why
			continue
		}
		return nil
	}
	return fmt.Errorf("%s after %v", last, timeout)
}

func versionsAt(vs mgmt.VersionStatus, want string) string {
	wv, err := twversion.Parse(want)
	if err != nil {
		return ""
	}
	for id, v := range vs.Mgmt {
		if sv, err := twversion.Parse(v); err != nil || sv.Compare(wv) != 0 {
			return "mgmt replica " + id + " runs " + v
		}
	}
	for v, n := range vs.Hosts {
		if sv, err := twversion.Parse(v); err != nil || sv.Compare(wv) != 0 {
			return fmt.Sprintf("%d host(s) run %q", n, v)
		}
	}
	return ""
}

func rollback(ctx context.Context, s *state, prefix, backup string, service, running bool) error {
	_ = stopTw(s, prefix, service, running)
	if err := restoreMgmt(s, backup); err != nil {
		return fmt.Errorf("restore %s: %w", backup, err)
	}
	if out, err := exec.CommandContext(ctx, "sh", filepath.Join(prefix, "install.sh"), "--prefix", prefix, "--rollback", "--force").CombinedOutput(); err != nil {
		return fmt.Errorf("install.sh --rollback: %v: %s", err, strings.Join(lastLinesOf(string(out), 3), " | "))
	}
	// The store went back in time: no lease epoch may be reused. Done here,
	// not by the restored release's tw-mgmt (an older one has no
	// restore-fence), and without migrating the restored store.
	if db := filepath.Join(s.Dir, "mgmt", "mgmt.db"); fileExists(db) {
		if _, err := mgmt.RestoreFenceDSN(ctx, db); err != nil {
			return fmt.Errorf("restore fence: %w", err)
		}
	}
	return startTw(s, prefix, service, running)
}

func lastLinesOf(s string, n int) []string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return ls
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// mergeEntries is a ∪ b by version (a wins), sorted.
func mergeEntries(a, b []catalog.Entry) []catalog.Entry {
	out := slices.Clone(a)
	for _, e := range b {
		if _, ok := catalog.Find(out, e.Version); !ok {
			out = append(out, e)
		}
	}
	catalog.Sort(out)
	return out
}
